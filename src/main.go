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
	"strings"
	"sync/atomic"

	_ "github.com/joho/godotenv/autoload"
)

const (
	apnsUpstreamTopic = "chat.rocket.ios"
	upstreamGateway   = "gateway.rocket.chat"
)

var (
	apnsTopic = os.Getenv("RCPG_APNS_TOPIC")
	debug, _  = strconv.ParseBool(os.Getenv("RCPG_DEBUG"))
	reqId     atomic.Uintptr
)

// Define a struct to hold the JSON payload
type RCPushNotification struct {
	Token   string `json:"token"`
	Options struct {
		CreatedAt string    `json:"createdAt"`
		CreatedBy string    `json:"createdBy"`
		Sent      bool      `json:"sent"`
		Sending   int       `json:"sending"`
		From      string    `json:"from"`
		Title     string    `json:"title"`
		Text      string    `json:"text"`
		UserId    string    `json:"userId"`
		Payload   RCPayload `json:"payload"`
		Badge     int       `json:"badge"`
		Sound     string    `json:"sound"`
		NotId     int       `json:"notId"`
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
	Rid              string `json:"rid,omitempty"`
	Sender           *struct {
		Id       string `json:"_id,omitempty"`
		Username string `json:"username,omitempty"`
		Name     string `json:"name,omitempty"`
	} `json:"sender,omitempty"`
	SenderName string `json:"senderName,omitempty"`
	Type       string `json:"type,omitempty"`
}

type rcRequest struct {
	id    uint
	http  *http.Request
	body  []byte
	data  RCPushNotification
	ejson []byte
}

type reqLogger struct {
	r *rcRequest
}

func l(r *rcRequest) reqLogger {
	return reqLogger{r}
}

func (l reqLogger) Printf(s string, v ...any) {
	id := fmt.Sprintf("[%d]", l.r.id)
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
	infoHandler := func(w http.ResponseWriter, req *http.Request) {
		log.Printf("InfoHandler for: %+v", req)
		io.WriteString(w, "Rocket.Chat Push Gateway\n")
	}
	http.HandleFunc("/", infoHandler)

	// Define the HTTP server and routes
	http.HandleFunc("/push/gcm/send", withRCRequest(getGCMPushNotificationHandler(), false))
	http.HandleFunc("/push/apn/send", withRCRequest(getAPNPushNotificationHandler(), false))
	http.HandleFunc("/filter/push/gcm/send", withRCRequest(getGCMPushNotificationHandler(), true))
	http.HandleFunc("/filter/push/apn/send", withRCRequest(getAPNPushNotificationHandler(), true))
	// Start the HTTP server
	addr := os.Getenv("RCPG_ADDR")
	log.Println("Starting server on", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("Failed to start server: ", err)
	}
}

func withRCRequest(handler func(http.ResponseWriter, *rcRequest), filter bool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, http_ *http.Request) {
		r := &rcRequest{http: http_}
		r.id = uint(reqId.Add(1))
		if r.http.Method != http.MethodPost {
			l(r).Errorf("Method not allowed: %v", r.http.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Read the request body
		var err error
		r.body, err = ioutil.ReadAll(http_.Body)
		if err != nil {
			l(r).Errorf("Failed to read request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		l(r).Debugf("Received push request: %+v %s", http_, r.body)

		// Parse the request body
		err = json.Unmarshal(r.body, &r.data)
		if err != nil {
			l(r).Errorf("Failed to parse request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		l(r).Printf("%s requested from %s", r.http.URL.RequestURI(), r.data.Options.Payload.Host)

		if filter && r.data.Options.Payload.NotificationType != "message-id-only" {
			r.data.Options.Title = ""
			r.data.Options.Text = "You have a new message"
			pl := r.data.Options.Payload
			r.data.Options.Payload = RCPayload{
				Host:             pl.Host,
				MessageId:        pl.MessageId,
				NotificationType: pl.NotificationType,
			}
			r.data.Options.Payload.NotificationType = "message-id-only"
		}

		r.ejson, _ = json.Marshal(r.data.Options.Payload)
		handler(w, r)
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func forward(w http.ResponseWriter, r *rcRequest) {
	l(r).Printf("Forwarding request to upstream")
	r.http.RequestURI = ""
	r.http.Host = ""
	r.http.URL.Scheme = "https"
	r.http.URL.Host = upstreamGateway
	path := r.http.URL.Path
	r.http.URL.Path = path[strings.LastIndex(path, "/push/"):]
	r.http.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(r.body)), nil
	}
	r.http.Body, _ = r.http.GetBody()
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
