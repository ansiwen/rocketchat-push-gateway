package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/joho/godotenv/autoload"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"
	"github.com/sideshow/apns2/payload"
)

// Define a struct to hold the JSON payload
type RCPushNotification struct {
	Token   string `json:"token"`
	Options struct {
		CreatedAt string `json:"createdAt"`
		CreatedBy string `json:"createdBy"`
		Sent      bool   `json:"sent"`
		Sending   int    `json:"sending"`
		From      string `json:"from"`
		Title     string `json:"title"`
		Text      string `json:"text"`
		UserId    string `json:"userId"`
		Payload   struct {
			Host             string `json:"host"`
			MessageId        string `json:"messageId"`
			NotificationType string `json:"notificationType"`
			Rid              string `json:"rid"`
			Sender           struct {
				Id       string `json:"_id"`
				Username string `json:"username"`
				Name     string `json:"name"`
			} `json:"sender"`
			SenderName string `json:"senderName"`
			Type       string `json:"type"`
		} `json:"payload"`
		Badge int    `json:"badge"`
		Sound string `json:"sound"`
		NotId int    `json:"notId"`
		Apn   struct {
			Category string `json:"category"`
			Text     string `json:"text"`
		} `json:"apn"`
		Gcm struct {
			Image string `json:"image"`
			Style string `json:"style"`
		} `json:"gcm"`
		Topic    string `json:"topic"`
		UniqueId string `json:"uniqueId"`
	} `json:"options"`
}

func main() {
	cert, err := certificate.FromP12File(
		os.Getenv("RCPG_APNS_CERT_FILE"),
		os.Getenv("RCPG_APNS_CERT_PASS"),
	)
	if err != nil {
		log.Fatal("Cert Error:", err)
	}
	client := apns2.NewClient(cert).Development()

	infoHandler := func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "Rocket.Chat Push Gateway\n")
	}
	http.HandleFunc("/", infoHandler)

	// Define the HTTP server and routes
	http.HandleFunc("/push/apn/send", getAPNPushNotificationHandler(client))
	// Start the HTTP server
	addr := os.Getenv("RCPG_ADDR")
	log.Printf("Starting server on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("Failed to start server: ", err)
	}
}

func getAPNPushNotificationHandler(client *apns2.Client) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Read the request body
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			log.Println("Failed to read request body: ", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		log.Printf("Received push request: %s", body)

		// Parse the request body
		var notification RCPushNotification
		err = json.Unmarshal(body, &notification)
		if err != nil {
			log.Println("Failed to parse request body: ", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		opt := &notification.Options
		// Create the notification payload
		p := payload.NewPayload().
			AlertTitle(opt.Title).
			AlertBody(opt.Text).
			Badge(opt.Badge).
			Sound(opt.Sound).
			MutableContent().
			ContentAvailable().
			Custom("ejson", opt.Payload)

		if opt.Apn.Category != "" {
			p.Category(opt.Apn.Category)
		}

		if opt.Apn.Text != "" {
			p.AlertBody(opt.Apn.Text)
		}

		// Create the notification
		n := &apns2.Notification{
			DeviceToken: notification.Token,
			Topic:       opt.Topic,
			Payload:     p,
		}

		// Send the notification
		res, err := client.Push(n)
		if err != nil {
			log.Println("Failed to send notification: ", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !res.Sent() {
			log.Println("Failed to send notification: ", res)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Log the response
		log.Println("Notification sent: ", res)

		// Send a success response
		w.WriteHeader(http.StatusOK)
	}
}

func handlePushNotification2(w http.ResponseWriter, r *http.Request, client apns2.Client) {
	// Read the request body
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Parse the request body as JSON
	var pushData RCPushNotification
	if err := json.Unmarshal(body, &pushData); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Create a new notification with the given payload and options
	payload := &payload.Payload{}

	notification := &apns2.Notification{
		DeviceToken: pushData.Token,
		Payload:     payload,
		Priority:    apns2.PriorityHigh,
		Expiration:  time.Now().Add(time.Hour * 1),
		ApnsID:      fmt.Sprintf("%d", pushData.Options.NotId),
		Topic:       pushData.Options.Topic,
		CollapseID:  pushData.Options.UniqueId,
	}

	// Send the push notification using the APNs client
	res, err := client.Push(notification)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return the response from the APNs server
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(res)
}
