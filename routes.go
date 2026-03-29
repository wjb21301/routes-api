package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	firebase "firebase.google.com/go"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
)

var firebaseApp *firebase.App

func initFirebase() {
	creds := os.Getenv("FIREBASE_CREDENTIALS")
	if creds == "" {
		log.Fatal("Missing FIREBASE_CREDENTIALS")
	}
	opt := option.WithAuthCredentialsJSON(option.ServiceAccount, []byte(creds))
	app, err := firebase.NewApp(context.Background(), nil, opt)
	if err != nil {
		log.Fatalf("error initializing firebase: %v\n", err)
	}
	firebaseApp = app
}

type RouteRequest struct {
	Origin      LatLng `json:"origin"`
	Destination LatLng `json:"destination"`
	Mode        string `json:"mode"`
}
type LatLng struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}
type RouteResponse struct {
	Duration string `json:"duration"`
	Distance string `json:"distance"`
	Polyline string `json:"polyline"`
}
type LatLngWrapper struct {
	LatLng LatLng `json:"latLng"`
}
type Waypoint struct {
	Location LatLngWrapper `json:"location"`
}
type GoogleRouteRequest struct {
	Origin      Waypoint `json:"origin"`
	Destination Waypoint `json:"destination"`
	TravelMode  string   `json:"travelMode"`
}

func routeTranslator(req RouteRequest) (GoogleRouteRequest, error) {
	var travelMode string
	switch req.Mode {
	case "driving":
		travelMode = "DRIVE"
	case "walking":
		travelMode = "WALK"
	default:
		return GoogleRouteRequest{}, fmt.Errorf("unsupported mode: %s", req.Mode)
	}
	googleReq := GoogleRouteRequest{
		Origin: Waypoint{
			Location: LatLngWrapper{
				LatLng: LatLng{
					Latitude:  req.Origin.Latitude,
					Longitude: req.Origin.Longitude,
				},
			},
		},
		Destination: Waypoint{
			Location: LatLngWrapper{
				LatLng: LatLng{
					Latitude:  req.Destination.Latitude,
					Longitude: req.Destination.Longitude,
				},
			},
		},
		TravelMode: travelMode,
	}

	return googleReq, nil
}
func RouteHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if firebaseApp == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Missing token"}`))
		return
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Invalid auth format"}`))
		return
	}

	idToken := strings.TrimPrefix(authHeader, "Bearer ")

	authClient, err := firebaseApp.Auth(context.Background())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	token, err := authClient.VerifyIDToken(context.Background(), idToken)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Invalid token"}`))
		return
	}
	log.Println("User authenticated:", token.UID)

	if r.Method != "POST" {
		w.WriteHeader(405)
		w.Write([]byte(`{"error": "Invalid send type"}`))
		return
	}
	req := RouteRequest{}
	decoder := json.NewDecoder(r.Body)
	err = decoder.Decode(&req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"error": "Invalid request"}`))
		return
	}
	if req.Mode == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"error": "You must have a mode of traveling."}`))
		return
	}
	if req.Origin.Latitude < -90 || req.Origin.Latitude > 90 || req.Origin.Longitude < -180 || req.Origin.Longitude > 180 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"error": "Invalid Coordinates"}`))
		return
	}
	if req.Destination.Latitude < -90 || req.Destination.Latitude > 90 || req.Destination.Longitude < -180 || req.Destination.Longitude > 180 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"error": "Invalid Coordinates"}`))
		return
	}
	googleReq, err := routeTranslator(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "Bad request"}`))
		return
	}
	apiKey := os.Getenv("GOOGLE_ROUTES_API_KEY")
	if apiKey == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "Missing API key"}`))
		return
	}
	payload, err := json.Marshal(googleReq)
	if err != nil {
		log.Println("Error marhshaliing google request:", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	googleURL := "https://routes.googleapis.com/directions/v2:computeRoutes"
	client := http.DefaultClient
	reqGoogle, err := http.NewRequest("POST", googleURL, bytes.NewReader(payload))
	if err != nil {
		log.Println("Error creating request:", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	reqGoogle.Header.Set("Content-Type", "application/json")
	reqGoogle.Header.Set("X-Goog-Api-Key", apiKey)
	reqGoogle.Header.Set("X-Goog-FieldMask", "routes.duration,routes.distanceMeters,routes.polyline.encodedPolyline")
	resp, err := client.Do(reqGoogle)
	if err != nil {
		log.Println("Error calling google routes: ", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Println("Google returned non-200:", resp.StatusCode, string(bodyBytes))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error": "Failed to fetch route from Google"}`))
		return
	}
	var googleResp struct {
		Routes []struct {
			Duration       string `json:"duration"`
			DistanceMeters int    `json:"distanceMeters"`
			Polyline       struct {
				EncodedPolyline string `json:"encodedPolyline"`
			} `json:"polyline"`
		} `json:"routes"`
	}
	err = json.NewDecoder(resp.Body).Decode(&googleResp)
	if err != nil {
		log.Println("Error decoding Google response:", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if len(googleResp.Routes) == 0 {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error": "No route found"}`))
		return
	}

	route := googleResp.Routes[0]
	secondsStr := strings.TrimSuffix(route.Duration, "s")
	secondsInt, err := strconv.Atoi(secondsStr)
	distInMiles := float64(route.DistanceMeters) * 0.000621371
	if err != nil {
		log.Println("Error parsing duration:", err)
		secondsInt = 0
	}
	formattedDuration := (time.Duration(secondsInt) * time.Second).String()
	routeResp := RouteResponse{
		Duration: formattedDuration,
		Distance: fmt.Sprintf("%.2f miles", distInMiles),
		Polyline: route.Polyline.EncodedPolyline,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(routeResp)
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found.")
	}
	initFirebase()
	http.HandleFunc("/route", RouteHandler)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Println("Server running on port", port)
	err = http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Fatal("Server Failed:", err)
	}

}
