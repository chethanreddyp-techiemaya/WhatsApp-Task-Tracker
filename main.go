package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	qrterminal "github.com/Baozisoftware/qrcode-terminal-go"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"
)

const (
	AirtableBaseID  = "appQdiMFIIjsEW2aM"
	AirtableAPIKey  = "pat3PvG5VFYBSFb5v.007ade4d95461e456d5045c8b77f80f3e2cc58154ac6cb9175285a98b0df0e53"
	AirtableTableID = "tblC9XMw5NHAH6JMy"
	OwnerJID        = "918712157587@s.whatsapp.net"
)

type AirtableRecord struct {
	Fields map[string]interface{} `json:"fields"`
}

var taskCommandRegex = regexp.MustCompile(`(?i)^Task\s+(.+?)\s*\|\s*(\d{4}-\d{2}-\d{2})\s*\|\s*(.+?)\s*\|\s*(\S+)\s*\|\s*(.+)`)

func parseTaskCommand(message string) (task, deadline, assignTo, attachment, description string, ok bool) {
	matches := taskCommandRegex.FindStringSubmatch(message)
	if len(matches) == 6 {
		return strings.TrimSpace(matches[1]), strings.TrimSpace(matches[2]), strings.TrimSpace(matches[3]), strings.TrimSpace(matches[4]), strings.TrimSpace(matches[5]), true
	}
	return
}

func addTaskToAirtable(task, deadline, assignTo, attachment, description string) error {
	url := fmt.Sprintf("https://api.airtable.com/v0/%s/%s", AirtableBaseID, AirtableTableID)
	record := AirtableRecord{
		Fields: map[string]interface{}{
			"Task":        task,
			"Deadline":    deadline,
			"Assign To":   assignTo,
			"Attachment":  []map[string]string{{"url": attachment}},
			"Description": description,
		},
	}
	jsonData, _ := json.Marshal(record)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+AirtableAPIKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("airtable error: %s", resp.Status)
	}
	return nil
}

func main() {
	startTime := time.Now()

	container, err := sqlstore.New(context.Background(), "sqlite", "file:session.db?cache=shared&_pragma=foreign_keys=on", waLog.Noop)
	if err != nil {
		panic(err)
	}
	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(err)
	}
	client := whatsmeow.NewClient(deviceStore, waLog.Noop)

	// Safe way to print JID
	if client.Store.ID != nil {
		fmt.Println("Your JID:", client.Store.ID.String())
	} else {
		fmt.Println("No existing session found. Need to scan QR code.")
	}

	if client.Store.ID == nil {
		// No existing session, need to scan QR code
		qrChan, _ := client.GetQRChannel(context.Background())
		err := client.Connect()
		if err != nil {
			fmt.Println("Failed to connect:", err)
			panic(err)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				fmt.Println("Scan QR Code:")
				qr := qrterminal.New()
				qr.Get(evt.Code).Print()
			} else if evt.Event == "success" {
				fmt.Println("Logged in successfully!")
				break
			} else if evt.Event == "timeout" {
				fmt.Println("QR code timeout. Please restart the application.")
				client.Disconnect()
				return
			}
		}
	} else {
		// Existing session found, try to connect
		err := client.Connect()
		if err != nil {
			fmt.Println("Failed to connect with existing session. Backing up and removing session, please restart to scan QR code again...")
			client.Disconnect()
			// Only back up and remove session.db if connection fails
			if _, statErr := os.Stat("session.db"); statErr == nil {
				backupName := fmt.Sprintf("session.db.backup.%d", time.Now().Unix())
				err := os.Rename("session.db", backupName)
				if err != nil {
					fmt.Println("Failed to back up session.db:", err)
				} else {
					fmt.Println("Backed up session.db to", backupName)
				}
			}
			fmt.Println("Please restart the application to scan QR code again.")
			return
		}
		fmt.Println("Connected with existing session.")
	}

	fmt.Println("WhatsApp Task Tracker is running...")

	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			if v.Info.MessageSource.IsFromMe || v.Message.GetConversation() == "" {
				return
			}
			if v.Info.Timestamp.Before(startTime) {
				return
			}
			text := strings.TrimSpace(v.Message.GetConversation())

			if _, _, _, _, _, ok := parseTaskCommand(text); ok {
				task, deadline, assignTo, attachment, description, _ := parseTaskCommand(text)
				fmt.Println("Received valid task command:", text)
				err := addTaskToAirtable(task, deadline, assignTo, attachment, description)
				senderName := v.Info.PushName
				if senderName == "" {
					senderName = v.Info.Sender.User
				}

				var reply string
				if err != nil {
					reply = fmt.Sprintf("%s - ❌ Failed to add task to Airtable: %s", senderName, err.Error())
				} else {
					reply = fmt.Sprintf("%s - ✅ Task added: %s | %s | %s", senderName, task, deadline, assignTo)
				}
				ownerJID, _ := types.ParseJID(OwnerJID)
				_, _ = client.SendMessage(context.Background(), ownerJID, &proto.Message{
					Conversation: &reply,
				})
			}
		}
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "WhatsApp Task Tracker is running")
		})
		http.ListenAndServe(":"+port, nil)
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	client.Disconnect()
}
