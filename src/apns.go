package main

import (
	"log"
	"net/http"
	"os"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"
	"github.com/sideshow/apns2/payload"
)

func getAPNPushNotificationHandler() func(http.ResponseWriter, *rcRequest) {
	cert, err := certificate.FromP12File(
		os.Getenv("RCPG_APNS_CERT_FILE"),
		os.Getenv("RCPG_APNS_CERT_PASS"),
	)
	if err != nil {
		log.Fatal("Cert Error:", err)
	}
	// apnsClient := apns2.NewClient(cert).Development()
	client := apns2.NewClient(cert).Production()

	return func(w http.ResponseWriter, r *rcRequest) {
		r.stats.apn.Add(1)

		opt := &r.data.Options

		if opt.Topic == apnsUpstreamTopic {
			forward(w, r)
			return
		}

		if opt.Topic != apnsTopic {
			r.Errorf("Unknown APNs topic: %s", opt.Topic)
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}

		// Create the notification payload
		p := payload.NewPayload().
			AlertTitle(opt.Title).
			AlertBody(opt.Text).
			Badge(opt.Badge).
			Sound(opt.Sound).
			Custom("ejson", string(r.ejson))

		if opt.Apn != nil {
			if opt.Apn.Category != "" {
				p.Category(opt.Apn.Category)
			}
			if opt.Apn.Text != "" {
				p.AlertBody(opt.Apn.Text)
			}
		}

		if opt.Payload != nil && opt.Payload.NotificationType == "message-id-only" {
			p.MutableContent()
		}

		// Create the notification
		n := &apns2.Notification{
			DeviceToken: r.data.Token,
			Topic:       opt.Topic,
			Payload:     p,
		}

		nJSON, _ := n.MarshalJSON()
		r.Debugf("Sending notification: %s", nJSON)

		// Send the notification
		res, err := client.Push(n)
		if err != nil {
			r.Errorf("Failed to send notification: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !res.Sent() {
			if res.Reason == apns2.ReasonBadDeviceToken ||
				res.Reason == apns2.ReasonDeviceTokenNotForTopic ||
				res.Reason == apns2.ReasonUnregistered {
				r.Printf("Deleting invalid token: %s", r.data.Token)
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			r.Errorf("Failed to send notification: %+v", res)
			w.WriteHeader(res.StatusCode)
			return
		}

		r.Debugf("Notification sent: %+v", res)

		w.WriteHeader(http.StatusOK)
		r.Printf("Notification sent to APNS")
	}
}
