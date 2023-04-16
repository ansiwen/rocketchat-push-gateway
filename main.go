package main

import (
	"bytes"
	"context"
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

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
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

type RCRequest struct {
	http    *http.Request
	body    []byte
	data    RCPushNotification
	payload RCPayload
}

type reqLogger struct {
	r *RCRequest
}

func l(r *RCRequest) reqLogger {
	return reqLogger{r}
}

func (l reqLogger) Printf(s string, v ...any) {
	id := fmt.Sprintf("[%#p]", l.r.http.Context())
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
	s = s + "\nRequest: %+v\nBody: %s"
	v = append(v, l.r.http, l.r.body)
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
	// apnsClient := apns2.NewClient(cert).Development()
	apnsClient := apns2.NewClient(cert).Production()

	opt := option.WithCredentialsFile(os.Getenv("RCPG_FCM_KEY_FILE"))
	app, err := firebase.NewApp(context.Background(), nil, opt)
	if err != nil {
		log.Fatalf("error initializing app: %v", err)
	}
	fcmClient, err := app.Messaging(context.Background())
	if err != nil {
		log.Fatalf("error initializing FCM client: %#v", err)
	}

	infoHandler := func(w http.ResponseWriter, req *http.Request) {
		log.Printf("InfoHandler for: %+v", req)
		io.WriteString(w, "Rocket.Chat Push Gateway\n")
	}
	http.HandleFunc("/", infoHandler)

	// Define the HTTP server and routes
	http.HandleFunc("/push/gcm/send", getGCMPushNotificationHandler(fcmClient))
	http.HandleFunc("/push/apn/send", getAPNPushNotificationHandler(apnsClient))
	// Start the HTTP server
	addr := os.Getenv("RCPG_ADDR")
	log.Println("Starting server on", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("Failed to start server: ", err)
	}
}

func parseRequest(w http.ResponseWriter, http_ *http.Request) *RCRequest {
	r := &RCRequest{http: http_}
	if http_.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return nil
	}

	// Read the request body
	var err error
	r.body, err = ioutil.ReadAll(http_.Body)
	if err != nil {
		l(r).Errorf("Failed to read request body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}

	l(r).Debugf("Received push request: %+v %s", http_, r.body)

	// Parse the request body
	err = json.Unmarshal(r.body, &r.data)
	if err != nil {
		l(r).Errorf("Failed to parse request body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}

	err = json.Unmarshal(r.data.Options.RawPayload, &r.payload)
	if err != nil {
		l(r).Errorf("Failed to parse request payload: %v", err)
	}

	return r
}

func getAPNPushNotificationHandler(client *apns2.Client) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, http_ *http.Request) {
		r := parseRequest(w, http_)
		if r == nil {
			return
		}

		opt := &r.data.Options

		if opt.Topic == apnsUpstreamTopic {
			forward(w, r)
			return
		}

		if opt.Topic != apnsTopic {
			l(r).Errorf("Unknown APNs topic: %s", opt.Topic)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		l(r).Printf("APN from %s to %s", r.payload.Host, opt.Topic)

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

		if r.payload.NotificationType == "message-id-only" {
			p.MutableContent()
			p.ContentAvailable()
		}

		// Create the notification
		n := &apns2.Notification{
			DeviceToken: r.data.Token,
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
			if res.Reason == apns2.ReasonBadDeviceToken ||
				res.Reason == apns2.ReasonDeviceTokenNotForTopic ||
				res.Reason == apns2.ReasonUnregistered {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			w.WriteHeader(res.StatusCode)
			return
		}

		// Log the response
		l(r).Debugf("Notification sent: %+v", res)

		// Send a success response
		w.WriteHeader(http.StatusOK)
	}
}

func getGCMPushNotificationHandler(client *messaging.Client) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, http_ *http.Request) {
		r := parseRequest(w, http_)
		if r == nil {
			return
		}

		l(r).Printf("GCM from %s", r.payload.Host)

		opt := r.data.Options

		data := map[string]string{
			"ejson":   string(opt.RawPayload),
			"title":   opt.Title,
			"message": opt.Text,
			"text":    opt.Text,
			"image":   opt.Gcm.Image,
			"msgcnt":  fmt.Sprint(opt.Badge),
			"sound":   opt.Sound,
			"notId":   fmt.Sprint(opt.NotId),
			"style":   opt.Gcm.Style,
		}

		msg := &messaging.Message{
			Token: r.data.Token,
			Android: &messaging.AndroidConfig{
				CollapseKey: opt.From,
				Priority:    "high",
				Data:        data,
			},
		}

		_, err := client.Send(context.Background(), msg)
		if err != nil {
			log.Printf("error sending FCM msg: %e", err)
			if messaging.IsUnregistered(err) {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			if messaging.IsSenderIDMismatch(err) {
				forward(w, r)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Send a success response
		w.WriteHeader(http.StatusOK)
		return
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func forward(w http.ResponseWriter, r *RCRequest) {
	l(r).Debugf("Forwarding request to upstream")
	r.http.RequestURI = ""
	r.http.Host = ""
	r.http.URL.Scheme = "https"
	r.http.URL.Host = upstreamGateway
	r.http.Body = io.NopCloser(bytes.NewReader(r.body))
	r.http.Header.Del("Connection")

	resp, err := http.DefaultClient.Do(r.http)
	if err != nil {
		l(r).Errorf("Failed to forward request: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	body, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	l(r).Debugf("Response from upstream: %+v %s", resp, body)
	copyHeader(w.Header(), resp.Header)
	if resp.StatusCode >= 300 {
		l(r).Errorf("Forwarding failed: %+v", resp)
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
