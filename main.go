package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"

	_ "github.com/joho/godotenv/autoload"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"
	"github.com/sideshow/apns2/payload"
)

const (
	debug             = true
	apnsUpstreamTopic = "chat.rocket.ios"
)

var apnsTopic = os.Getenv("RCPG_APNS_TOPIC")

// Define a struct to hold the JSON payload
type RCPushNotification struct {
	Token   string `json:"token"`
	Options struct {
		CreatedAt string      `json:"createdAt"`
		CreatedBy string      `json:"createdBy"`
		Sent      bool        `json:"sent"`
		Sending   int         `json:"sending"`
		From      string      `json:"from"`
		Title     string      `json:"title"`
		Text      string      `json:"text"`
		UserId    string      `json:"userId"`
		Payload   interface{} `json:"payload"`
		Badge     int         `json:"badge"`
		Sound     string      `json:"sound"`
		NotId     int         `json:"notId"`
		Apn       struct {
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

type RCPayload struct {
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
}

type reqLogger struct {
	r *http.Request
}

func l(r *http.Request) reqLogger {
	return reqLogger{r}
}

func (l reqLogger) Printf(s string, v ...any) {
	id := fmt.Sprintf("[%#p]", l.r.Context())
	s = id + " " + s
	log.Printf(s, v...)
}

func (l reqLogger) Debugf(s string, v ...any) {
	if !debug {
		return
	}
	l.Printf(s, v...)
}

func (l reqLogger) Errorf(s string, v ...any) {
	l.Printf(s, v...)
}

func main() {
	cert, err := certificate.FromP12File(
		os.Getenv("RCPG_APNS_CERT_FILE"),
		os.Getenv("RCPG_APNS_CERT_PASS"),
	)
	if err != nil {
		log.Fatal("Cert Error:", err)
	}
	// client := apns2.NewClient(cert).Development()
	client := apns2.NewClient(cert).Production()

	infoHandler := func(w http.ResponseWriter, req *http.Request) {
		log.Printf("InfoHandler for: %+v", req)
		io.WriteString(w, "Rocket.Chat Push Gateway\n")
	}
	http.HandleFunc("/", infoHandler)

	// Define the HTTP server and routes
	http.HandleFunc("/push/gcm/send", GCMPushNotificationHandler)
	http.HandleFunc("/push/apn/send", getAPNPushNotificationHandler(client))
	// Start the HTTP server
	addr := os.Getenv("RCPG_ADDR")
	log.Println("Starting server on", addr)
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
			l(r).Errorf("Failed to read request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		l(r).Debugf("Received push request: %+v %s", r, body)

		// Parse the request body
		var notification RCPushNotification
		err = json.Unmarshal(body, &notification)
		if err != nil {
			l(r).Errorf("Failed to parse request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		opt := &notification.Options

		if opt.Topic == apnsUpstreamTopic {
			forward(w, r, body)
			return
		}

		if opt.Topic != apnsTopic {
			l(r).Errorf("Unknown APNs topic: %s", opt.Topic)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		ejson, _ := json.Marshal(opt.Payload)

		var rcPayload RCPayload
		err = json.Unmarshal(ejson, &rcPayload)
		if err != nil {
			l(r).Errorf("Failed to parse request payload: %v", err)
		}

		// Create the notification payload
		p := payload.NewPayload().
			AlertTitle(opt.Title).
			AlertBody(opt.Text).
			Badge(opt.Badge).
			Sound(opt.Sound).
			Custom("ejson", string(ejson))

		if opt.Apn.Category != "" {
			p.Category(opt.Apn.Category)
		}

		if opt.Apn.Text != "" {
			p.AlertBody(opt.Apn.Text)
		}

		if rcPayload.NotificationType == "message-id-only" {
			p.MutableContent()
			p.ContentAvailable()
		}

		// Create the notification
		n := &apns2.Notification{
			DeviceToken: notification.Token,
			Topic:       opt.Topic,
			Payload:     p,
		}

		nJSON, _ := n.MarshalJSON()
		l(r).Debugf("Sending notification: %s", nJSON)

		// Send the notification
		res, err := client.Push(n)
		if err != nil {
			l(r).Errorf("Failed to send notification: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !res.Sent() {
			l(r).Errorf("Failed to send notification: %+v", res)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Log the response
		l(r).Debugf("Notification sent: %+v", res)

		// Send a success response
		w.WriteHeader(http.StatusOK)
	}
}

func GCMPushNotificationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the request body
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		l(r).Errorf("Failed to read request body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	l(r).Debugf("Received push request: %s", body)

	forward(w, r, body)
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func forward(w http.ResponseWriter, r *http.Request, body []byte) {
	l(r).Debugf("Forwarding request: %+v", r)
	r.RequestURI = ""
	r.Host = ""
	r.URL.Scheme = "https"
	r.URL.Host = "gateway.rocket.chat"
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.Header.Del("Connection")

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		l(r).Errorf("Failed to forward request: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	body, _ = ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	l(r).Debugf("Response from upstream: %+v %s", resp, body)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
