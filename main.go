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
	"strconv"

	_ "github.com/joho/godotenv/autoload"
	"github.com/sideshow/apns2"
	apnsCertificate "github.com/sideshow/apns2/certificate"
	apnsPayload "github.com/sideshow/apns2/payload"
)

const (
	apnsUpstreamTopic = "chat.rocket.ios"
	upstreamGateway   = "gateway.rocket.chat"
)

var (
	apnsTopic = os.Getenv("RCPG_APNS_TOPIC")
	debug, _  = strconv.ParseBool(os.Getenv("RCPG_DEBUG"))
)

// Define a struct to hold the JSON payload
type RCPushNotification struct {
	Token   string `json:"token"`
	Options struct {
		CreatedAt  string          `json:"createdAt"`
		CreatedBy  string          `json:"createdBy"`
		Sent       bool            `json:"sent"`
		Sending    int             `json:"sending"`
		From       string          `json:"from"`
		Title      string          `json:"title"`
		Text       string          `json:"text"`
		UserId     string          `json:"userId"`
		RawPayload json.RawMessage `json:"payload"`
		Badge      int             `json:"badge"`
		Sound      string          `json:"sound"`
		NotId      int             `json:"notId"`
		Apn        struct {
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
	l.Printf("Error for request %+v", l.r)
	l.Printf(s, v...)
}

func main() {
	cert, err := apnsCertificate.FromP12File(
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

func parseRequest(w http.ResponseWriter, r *http.Request) ([]byte, *RCPushNotification, *RCPayload) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return nil, nil, nil
	}

	// Read the request body
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		l(r).Errorf("Failed to read request body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return nil, nil, nil
	}

	l(r).Debugf("Received push request: %+v %s", r, body)

	// Parse the request body
	var notification RCPushNotification
	err = json.Unmarshal(body, &notification)
	if err != nil {
		l(r).Errorf("Failed to parse request body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return nil, nil, nil
	}

	var rcPayload RCPayload
	err = json.Unmarshal(notification.Options.RawPayload, &rcPayload)
	if err != nil {
		l(r).Errorf("Failed to parse request payload: %v", err)
	}

	return body, &notification, &rcPayload
}

func getAPNPushNotificationHandler(client *apns2.Client) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		body, notification, rcPayload := parseRequest(w, r)
		if body == nil {
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

		l(r).Printf("APN from %s to %s", rcPayload.Host, opt.Topic)

		// Create the notification payload
		p := apnsPayload.NewPayload().
			AlertTitle(opt.Title).
			AlertBody(opt.Text).
			Badge(opt.Badge).
			Sound(opt.Sound).
			Custom("ejson", string(opt.RawPayload))

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
	body, _, rcPayload := parseRequest(w, r)
	if body == nil {
		return
	}

	l(r).Printf("GCM from %s", rcPayload.Host)

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
	l(r).Debugf("Forwarding request to upstream")
	r.RequestURI = ""
	r.Host = ""
	r.URL.Scheme = "https"
	r.URL.Host = upstreamGateway
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
	if resp.StatusCode >= 300 {
		l(r).Errorf("Forwarding failed: %+v", resp)
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
