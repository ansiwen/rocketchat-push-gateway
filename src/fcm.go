package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

func getGCMPushNotificationHandler() func(http.ResponseWriter, *rcRequest) {
	opt := option.WithCredentialsFile(os.Getenv("RCPG_FCM_KEY_FILE"))
	app, err := firebase.NewApp(context.Background(), nil, opt)
	if err != nil {
		log.Fatalf("error initializing app: %v", err)
	}
	client, err := app.Messaging(context.Background())
	if err != nil {
		log.Fatalf("error initializing FCM client: %v", err)
	}

	return func(w http.ResponseWriter, r *rcRequest) {
		r.stats.fcm.Add(1)

		opt := r.data.Options

		data := map[string]string{
			"ejson":   string(r.ejson),
			"title":   opt.Title,
			"message": opt.Text,
			"msgcnt":  fmt.Sprint(opt.Badge),
			"sound":   opt.Sound,
			"notId":   fmt.Sprint(opt.NotID),
			"image":   "",
			"style":   "",
		}

		if opt.Gcm != nil {
			data["image"] = opt.Gcm.Image
			data["style"] = opt.Gcm.Style
		}

		msg := &messaging.Message{
			Token: r.data.Token,
			Android: &messaging.AndroidConfig{
				CollapseKey: opt.From,
				Priority:    "high",
				Data:        data,
			},
		}

		msgJSON, _ := json.Marshal(msg)
		r.Debugf("Sending notification: %s", msgJSON)

		_, err := client.Send(context.Background(), msg)
		if err != nil {
			if messaging.IsUnregistered(err) {
				r.Printf("Deleting invalid token: %s", r.data.Token)
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			if messaging.IsSenderIDMismatch(err) {
				forward(w, r)
				return
			}
			r.Errorf("error sending FCM msg: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
		r.Printf("Notification sent to FCM")
	}
}
