package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	reqID     atomic.Uintptr
)

// RCPushNotification is a struct to hold the JSON payload
type RCPushNotification struct {
	Token   string `json:"token"`
	Options struct {
		CreatedAt string     `json:"createdAt"`
		CreatedBy string     `json:"createdBy"`
		Sent      bool       `json:"sent"`
		Sending   int        `json:"sending"`
		From      string     `json:"from"`
		Title     string     `json:"title"`
		Text      string     `json:"text"`
		UserID    string     `json:"userId"`
		Payload   *RCPayload `json:"payload,omitempty"`
		Badge     int        `json:"badge,omitempty"`
		Sound     string     `json:"sound"`
		NotID     int        `json:"notId,omitempty"`
		Apn       *struct {
			Category string `json:"category,omitempty"`
			Text     string `json:"text,omitempty"`
		} `json:"apn,omitempty"`
		Gcm *struct {
			Image string `json:"image,omitempty"`
			Style string `json:"style,omitempty"`
		} `json:"gcm,omitempty"`
		Topic    string `json:"topic,omitempty"`
		UniqueID string `json:"uniqueId"`
	} `json:"options"`
}

type RCPayload struct {
	Host             string `json:"host"`
	MessageID        string `json:"messageId"`
	NotificationType string `json:"notificationType"`
	Rid              string `json:"rid,omitempty"`
	Sender           *struct {
		ID       string `json:"_id,omitempty"`
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
	stats *status
}

func (r *rcRequest) Printf(s string, v ...any) {
	id := fmt.Sprintf("[%d]", r.id)
	s = id + " " + s
	log.Printf(s, v...)
}

func (r *rcRequest) Debugf(s string, v ...any) {
	if !debug {
		return
	}
	r.Printf(s, v...)
}

func (r *rcRequest) Errorf(s string, v ...any) {
	s = s + "\nRequest: %+v"
	v = append(v, r.http)
	s = s + "\nBody: %s"
	v = append(v, r.body)
	r.Printf(s, v...)
}

func getIP(r *http.Request) string {
	var ip string
	fwdHdr := r.Header["X-Forwarded-For"]
	if len(fwdHdr) == 0 {
		ip = strings.Split(r.RemoteAddr, ":")[0]
	} else {
		ip = fwdHdr[0]
	}
	return ip
}

var infoText = `
<!DOCTYPE html>
<html><head>
<title>Rocket.Chat Push Gateway</title>
</head><body>
<h2>Rocket.Chat Push Gateway</h2>
<p>See <a href="https://github.com/ansiwen/rocketchat-push-gateway">
https://github.com/ansiwen/rocketchat-push-gateway</a></p>
</body></html>
`

func main() {
	infoHandler := func(w http.ResponseWriter, req *http.Request) {
		log.Printf("InfoHandler for %s from %s", req.RequestURI, getIP(req))
		io.WriteString(w, infoText)
	}
	http.HandleFunc("/", infoHandler)

	http.HandleFunc("/stats", statsHandler)

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
		r.id = uint(reqID.Add(1))
		if r.http.Method != http.MethodPost {
			r.Errorf("Method not allowed: %v", r.http.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Read the request body
		var err error
		r.body, err = io.ReadAll(http_.Body)
		if err != nil {
			r.Errorf("Failed to read request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		r.Debugf("Received push request: %+v %s", http_, r.body)

		// Parse the request body
		err = json.Unmarshal(r.body, &r.data)
		if err != nil {
			r.Errorf("Failed to parse request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var host string

		if r.data.Options.Payload != nil {
			host = r.data.Options.Payload.Host
			if filter && r.data.Options.Payload.NotificationType == "message" {
				r.data.Options.Title = ""
				r.data.Options.Text = "You have a new message"
				pl := r.data.Options.Payload
				r.data.Options.Payload = &RCPayload{
					Host:             pl.Host,
					MessageID:        pl.MessageID,
					NotificationType: "message-id-only",
				}
				r.body = nil
			}
			r.ejson, _ = json.Marshal(r.data.Options.Payload)
		}

		ip := getIP(r.http)

		r.stats = getStats(r.data.Options.UniqueID, ip, host)

		r.Printf("%s requested from %s;Id:%s;Host:%s",
			r.http.URL.RequestURI(),
			ip,
			r.data.Options.UniqueID,
			host)

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
	r.stats.forwarded.Add(1)

	if r.stats.isDisabled() {
		r.Printf("Forwarding disabled")
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	r.http.RequestURI = ""
	r.http.Host = ""
	r.http.URL.Scheme = "https"
	r.http.URL.Host = upstreamGateway
	path := r.http.URL.Path
	r.http.URL.Path = path[strings.LastIndex(path, "/push/"):]
	if r.body == nil {
		var err error
		r.body, err = json.Marshal(r.data)
		if err != nil {
			r.Errorf("Failed to create filtered body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.http.Header.Del("Content-Length")
		r.http.ContentLength = int64(len(r.body))
	}
	r.http.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(r.body)), nil
	}
	r.http.Body, _ = r.http.GetBody()
	r.http.Header.Del("Connection")

	resp, err := http.DefaultClient.Do(r.http)
	if err != nil {
		r.Errorf("Failed to forward request: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	r.Debugf("Response from upstream: %+v %s", resp, body)
	copyHeader(w.Header(), resp.Header)
	if resp.StatusCode >= 300 {
		r.Printf("Forwarding failed: %s %s", resp.Status, body)
		if resp.StatusCode == 422 {
			r.stats.disable()
		}
	} else {
		r.Printf("Forwarded request to upstream")
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
