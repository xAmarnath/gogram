package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/xAmarnath/gogram/telegram"
)

func main() {
	log.SetFlags(0)

	raw, err := os.ReadFile("session.private.json")
	if err != nil {
		log.Fatal("Read session.private.json: ", err)
	}

	var saved struct {
		StringSession string `json:"string_session"`
		DataCenter    int    `json:"data_center"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		log.Fatal("Invalid session JSON: ", err)
	}
	saved.StringSession = strings.TrimSpace(saved.StringSession)
	if saved.StringSession == "" {
		log.Fatal("session.private.json is missing string_session")
	}

	var session telegram.Session
	if err := session.Decode(saved.StringSession); err != nil {
		log.Fatal("Invalid gogram string session")
	}

	input := bufio.NewScanner(os.Stdin)
	phonePattern := regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)
	contacts := make([]*telegram.InputPhoneContact, 0)
	seen := make(map[string]bool)

	fmt.Println("Enter contacts to import. Leave the phone blank to finish and upload.")
	for {
		fmt.Print("Phone number (+country code, Enter to finish): ")
		if !input.Scan() {
			break
		}
		phone := strings.TrimSpace(input.Text())
		if phone == "" {
			break
		}
		if !phonePattern.MatchString(phone) {
			fmt.Println("Use international format with + and digits, for example +15555550123.")
			continue
		}
		if seen[phone] {
			fmt.Println("That number is already in this batch.")
			continue
		}

		fmt.Print("First name: ")
		if !input.Scan() {
			log.Fatal("Input ended before the first name")
		}
		firstName := strings.TrimSpace(input.Text())
		if firstName == "" {
			fmt.Println("Enter a first name; please enter this contact again.")
			continue
		}

		fmt.Print("Last name (optional): ")
		if !input.Scan() {
			log.Fatal("Input ended before the last name")
		}
		lastName := strings.TrimSpace(input.Text())

		contacts = append(contacts, &telegram.InputPhoneContact{
			ClientID:  int64(len(contacts) + 1),
			Phone:     phone,
			FirstName: firstName,
			LastName:  lastName,
		})
		seen[phone] = true
		fmt.Printf("Queued %d contact(s).\n", len(contacts))
	}
	if err := input.Err(); err != nil {
		log.Fatal("Read contacts: ", err)
	}
	if len(contacts) == 0 {
		fmt.Println("No contacts entered.")
		return
	}

	client, err := telegram.NewClient(telegram.ClientConfig{
		AppID:         session.AppID,
		StringSession: saved.StringSession,
		DataCenter:    saved.DataCenter,
		IpAddr:        session.Hostname,
		MemorySession: true,
		NoPreconnect:  true,
		NoUpdates:     true,
		DisableCache:  true,
		Timeout:       30,
		ReqTimeout:    30,
		LogLevel:      telegram.LogError,
		FloodHandler:  func(error) bool { return false },
		ErrorHandler:  func(error) bool { return false },
	})
	if err != nil {
		log.Fatal("Create client: ", err)
	}
	defer client.Terminate()

	if err := client.Connect(); err != nil {
		log.Fatal("Connect: ", err)
	}
	me, err := client.GetMe()
	if err != nil {
		log.Fatal("The session must belong to a logged-in user: ", err)
	}
	if me.Bot {
		log.Fatal("Contact import requires a user session")
	}
	fmt.Printf("Connected as %s (user ID %d).\n", me.FirstName, me.ID)

	importedCount := 0
	retryIDs := make([]int64, 0)
	for start := 0; start < len(contacts); start += 500 {
		end := min(start+500, len(contacts))
		fmt.Printf("Importing contacts %d-%d of %d...\n", start+1, end, len(contacts))
		result, err := client.ContactsImportContacts(contacts[start:end])
		if err != nil {
			log.Fatalf("Import failed for contacts %d-%d: %v", start+1, end, err)
		}
		fmt.Println(client.JSON(result))
		importedCount += len(result.Imported)
		retryIDs = append(retryIDs, result.RetryContacts...)
	}

	fmt.Printf("Submitted %d contacts; returned %d Telegram user matches.\n", len(contacts), importedCount)
	if len(retryIDs) > 0 {
		fmt.Printf("Retry these client IDs later: %v\n", retryIDs)
	}
}
