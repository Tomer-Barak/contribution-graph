package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Configuration - Set these via environment variables
var (
	GitHubUsername = os.Getenv("GITHUB_USERNAME")
	GitHubToken    = os.Getenv("GITHUB_TOKEN")
	LocalServerURL = getEnv("SERVER_URL", "http://localhost:8080/api/contributions")
)

// GitHub GraphQL Query
const query = `
{
  user(login: "%s") {
    contributionsCollection {
      contributionCalendar {
        totalContributions
        weeks {
          contributionDays {
            date
            contributionCount
          }
        }
      }
    }
  }
}
`

// Structs to parse GitHub Response
type GitHubResponse struct {
	Data struct {
		User struct {
			ContributionsCollection struct {
				ContributionCalendar struct {
					TotalContributions int `json:"totalContributions"`
					Weeks              []struct {
						ContributionDays []struct {
							Date              string `json:"date"`
							ContributionCount int    `json:"contributionCount"`
						} `json:"contributionDays"`
					} `json:"weeks"`
				} `json:"contributionCalendar"`
			} `json:"contributionsCollection"`
		} `json:"user"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Your App's Schema
type Contribution struct {
	Source    string   `json:"source"`
	Context   string   `json:"context"`
	Timestamp string   `json:"timestamp"`
	MetaData  MetaData `json:"metadata"`
}

type MetaData struct {
	ImportedFromGitHub bool   `json:"imported_from_github"`
	OriginalCount      int    `json:"original_count,omitempty"`
	ImportDate         string `json:"import_date"`
}

func main() {
	// Validate configuration
	if GitHubUsername == "" {
		fmt.Println("❌ Error: GITHUB_USERNAME environment variable is required")
		fmt.Println("\nUsage:")
		fmt.Println("  export GITHUB_USERNAME=your-username")
		fmt.Println("  export GITHUB_TOKEN=your-personal-access-token")
		fmt.Println("  go run main.go")
		os.Exit(1)
	}

	if GitHubToken == "" {
		fmt.Println("❌ Error: GITHUB_TOKEN environment variable is required")
		fmt.Println("\nCreate a Personal Access Token at:")
		fmt.Println("  https://github.com/settings/tokens")
		fmt.Println("  (Select 'read:user' scope)")
		os.Exit(1)
	}

	fmt.Printf("🚀 Fetching contribution data from GitHub for user: %s\n", GitHubUsername)

	// Prepare the GraphQL Request
	q := fmt.Sprintf(query, GitHubUsername)
	reqBody, _ := json.Marshal(map[string]string{"query": q})
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewBuffer(reqBody))
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", "Bearer "+GitHubToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "contribution-graph-importer")

	// Send Request to GitHub
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ Error fetching from GitHub: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Printf("❌ GitHub API returned status %d\n", resp.StatusCode)
		os.Exit(1)
	}

	// Parse Response
	var ghResp GitHubResponse
	if err := json.NewDecoder(resp.Body).Decode(&ghResp); err != nil {
		fmt.Printf("❌ Error parsing JSON: %v\n", err)
		os.Exit(1)
	}

	if len(ghResp.Errors) > 0 {
		fmt.Printf("❌ GitHub API Error: %s\n", ghResp.Errors[0].Message)
		os.Exit(1)
	}

	// Convert to Your App's Format
	var myContributions []Contribution
	calendar := ghResp.Data.User.ContributionsCollection.ContributionCalendar
	importDate := time.Now().Format(time.RFC3339)

	fmt.Printf("📊 Total contributions in the last year: %d\n", calendar.TotalContributions)

	for _, week := range calendar.Weeks {
		for _, day := range week.ContributionDays {
			if day.ContributionCount == 0 {
				continue
			}

			// Parse date (YYYY-MM-DD)
			t, err := time.Parse("2006-01-02", day.Date)
			if err != nil {
				continue
			}

			// Create individual events for each contribution
			// This ensures proper heatmap intensity
			for i := 0; i < day.ContributionCount; i++ {
				// Spread timestamps slightly to avoid exact duplicates
				timestamp := t.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)

				c := Contribution{
					Source:    "github-import",
					Context:   "github-history",
					Timestamp: timestamp,
					MetaData: MetaData{
						ImportedFromGitHub: true,
						OriginalCount:      day.ContributionCount,
						ImportDate:         importDate,
					},
				}
				myContributions = append(myContributions, c)
			}
		}
	}

	fmt.Printf("✅ Prepared %d contribution events for import\n", len(myContributions))

	// Check if dry run
	if os.Getenv("DRY_RUN") == "true" {
		jsonData, _ := json.MarshalIndent(myContributions[:min(5, len(myContributions))], "", "  ")
		fmt.Println("\n📄 Dry run - Sample output (first 5 events):")
		fmt.Println(string(jsonData))
		fmt.Printf("\n... and %d more events\n", len(myContributions)-5)
		return
	}

	// Push to Your Local Server
	if len(myContributions) > 0 {
		err := pushToServer(myContributions)
		if err != nil {
			fmt.Printf("❌ Error uploading: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("🎉 Successfully imported GitHub history to your local app!")
	}
}

func pushToServer(data []Contribution) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	resp, err := http.Post(LocalServerURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	return nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
