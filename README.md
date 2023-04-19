# rocketchat-push-gateway

A simple push notification gateway for Rocket.Chat servers written in Go. 

```
docker run -d -v /path/to/push/secrets:/data -e RCPG_APNS_CERT_PASS=... ansiwen/rocketchat-push-gateway
```
