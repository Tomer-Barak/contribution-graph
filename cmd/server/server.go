package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// Contribution represents the unified event structure
type Contribution struct {
	Source    string          `json:"source"`
	Context   string          `json:"context"`
	Timestamp time.Time       `json:"timestamp"`
	MetaData  json.RawMessage `json:"metadata"`
}

var db *sql.DB

func main() {
	var err error

	// Database path from env or default
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/contributions.db"
	}

	// Ensure data directory exists
	if err := os.MkdirAll("./data", 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Initialize SQLite Database
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Create the Events Table
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source TEXT NOT NULL,
		context TEXT,
		timestamp DATETIME NOT NULL,
		metadata TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(source, context, timestamp)
	);
	CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_events_source ON events(source);
	`

	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	// Create a new ServeMux for routing
	mux := http.NewServeMux()

	// API Routes - using method checks inside handlers for compatibility
	mux.HandleFunc("/api/contributions", handleContributions)
	mux.HandleFunc("/api/stats", handleGetStats)
	mux.HandleFunc("/api/health", handleHealth)

	// Serve static files
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "./static"
	}
	mux.Handle("/", http.FileServer(http.Dir(staticDir)))

	// Get port from env or default
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// CORS middleware wrapper
	handler := corsMiddleware(mux)

	fmt.Printf("🚀 Contribution Graph Server running on http://localhost:%s\n", port)
	fmt.Println("   Dashboard: /")
	fmt.Println("   Mobile:    /mobile.html")
	fmt.Println("   API:       POST /api/contributions")
	log.Fatal(http.ListenAndServe(":"+port, handler))
}

// CORS Middleware for cross-origin requests
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		// Prevent caching of API responses for faster year switching
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Combined handler for /api/contributions (routes GET vs POST)
func handleContributions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleGetContributions(w, r)
	case http.MethodPost:
		handlePostContribution(w, r)
	case http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// POST: Receive new events
func handlePostContribution(w http.ResponseWriter, r *http.Request) {
	var contributions []Contribution

	// Decode JSON body
	if err := json.NewDecoder(r.Body).Decode(&contributions); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO events (source, context, timestamp, metadata) 
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		tx.Rollback()
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer stmt.Close()

	count := 0
	for _, c := range contributions {
		metaString := string(c.MetaData)
		if metaString == "" {
			metaString = "{}"
		}

		_, err := stmt.Exec(c.Source, c.Context, c.Timestamp, metaString)
		if err == nil {
			count++
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"processed": count,
		"message":   fmt.Sprintf("Processed %d contributions", count),
	})

	fmt.Printf("📥 Received %d events (from %d submitted)\n", count, len(contributions))
}

// GET: Retrieve contributions for the frontend
func handleGetContributions(w http.ResponseWriter, r *http.Request) {
	// Query parameters
	year := r.URL.Query().Get("year")
	source := r.URL.Query().Get("source")

	// Default to current year
	if year == "" {
		year = fmt.Sprintf("%d", time.Now().Year())
	}

	// Parse year for range-based filtering (much faster than substr)
	yearInt, err := fmt.Sscanf(year, "%d", new(int))
	if err != nil || yearInt != 1 {
		http.Error(w, "Invalid year parameter", http.StatusBadRequest)
		return
	}

	// Build query using range-based filtering for better index usage
	startDate := year + "-01-01"
	endDate := fmt.Sprintf("%d-01-01", mustAtoi(year)+1)

	query := `
		SELECT source, context, timestamp, metadata 
		FROM events 
		WHERE timestamp >= ? AND timestamp < ?
	`
	args := []interface{}{startDate, endDate}

	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
	}

	query += " ORDER BY timestamp DESC"

	rows, err := db.Query(query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var events []Contribution
	for rows.Next() {
		var c Contribution
		var metaString string
		var ts time.Time

		if err := rows.Scan(&c.Source, &c.Context, &ts, &metaString); err != nil {
			continue
		}
		c.Timestamp = ts
		c.MetaData = json.RawMessage(metaString)
		events = append(events, c)
	}

	// Return empty array instead of null
	if events == nil {
		events = []Contribution{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

// GET: Statistics endpoint
func handleGetStats(w http.ResponseWriter, r *http.Request) {
	stats := make(map[string]interface{})

	// Total contributions
	var total int
	db.QueryRow("SELECT COUNT(*) FROM events").Scan(&total)
	stats["total"] = total

	// Contributions by source
	rows, err := db.Query(`
		SELECT source, COUNT(*) as count 
		FROM events 
		GROUP BY source 
		ORDER BY count DESC
	`)
	if err == nil {
		defer rows.Close()
		sources := make(map[string]int)
		for rows.Next() {
			var source string
			var count int
			rows.Scan(&source, &count)
			sources[source] = count
		}
		stats["by_source"] = sources
	}

	// Current streak
	streak := calculateStreak()
	stats["current_streak"] = streak

	// Today's contributions
	var today int
	db.QueryRow(`
		SELECT COUNT(*) FROM events 
		WHERE date(timestamp) = date('now')
	`).Scan(&today)
	stats["today"] = today

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// Calculate current contribution streak
func calculateStreak() int {
	rows, err := db.Query(`
		SELECT DISTINCT date(timestamp) as day
		FROM events
		ORDER BY day DESC
		LIMIT 365
	`)
	if err != nil {
		return 0
	}
	defer rows.Close()

	streak := 0
	expectedDate := time.Now().Truncate(24 * time.Hour)

	for rows.Next() {
		var dayStr string
		rows.Scan(&dayStr)
		day, err := time.Parse("2006-01-02", dayStr)
		if err != nil {
			continue
		}

		// Check if this day matches expected
		if day.Equal(expectedDate) || day.Equal(expectedDate.AddDate(0, 0, -1)) {
			streak++
			expectedDate = day.AddDate(0, 0, -1)
		} else if day.Before(expectedDate) {
			// Gap found, streak ends
			break
		}
	}

	return streak
}

// Health check endpoint
func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// mustAtoi converts string to int, panics on error (for validated input)
func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}
