package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/burntcarrot/pairpad/commons"
	"github.com/burntcarrot/pairpad/crdt"
	"github.com/gorilla/websocket"
	"github.com/nsf/termbox-go"
	"github.com/sirupsen/logrus"
)

// handleTermboxEvent handles key input by updating the local CRDT document
// and sending a message over the WebSocket connection.
func handleTermboxEvent(ev termbox.Event, conn *websocket.Conn) error {
	// We only want to deal with termbox key events (EventKey).
	if ev.Type == termbox.EventKey {
		switch ev.Key {

		// The default keys for exiting an session are Esc and Ctrl+C.
		case termbox.KeyEsc, termbox.KeyCtrlC:
			// Return an error with the prefix "pairpad", so that it gets treated as an exit "event".
			return errors.New("pairpad: exiting")

		// The default key for saving the editor's contents is Ctrl+S.
		case termbox.KeyCtrlS:
			// If no file name is specified, set filename to "pairpad-content.txt"
			if fileName == "" {
				fileName = "pairpad-content.txt"
			}

			// Save the CRDT to a file.
			err := crdt.Save(fileName, &doc)
			if err != nil {
				logrus.Errorf("Failed to save to %s", fileName)
				e.StatusChan <- fmt.Sprintf("Failed to save to %s", fileName)
				return err
			}

			// Set the status bar.
			e.StatusChan <- fmt.Sprintf("Saved document to %s", fileName)

		// The default key for loading content from a file is Ctrl+L.
		case termbox.KeyCtrlL:
			if fileName != "" {
				logger.Log(logrus.InfoLevel, "LOADING DOCUMENT")
				newDoc, err := crdt.Load(fileName)
				if err != nil {
					logrus.Errorf("failed to load file %s", fileName)
					e.StatusChan <- fmt.Sprintf("Failed to load %s", fileName)
					return err
				}
				e.StatusChan <- fmt.Sprintf("Loading %s", fileName)
				doc = newDoc
				e.SetX(0)
				e.SetText(crdt.Content(doc))

				logger.Log(logrus.InfoLevel, "SENDING DOCUMENT")
				docMsg := commons.Message{Type: commons.DocSyncMessage, Document: doc}
				_ = conn.WriteJSON(&docMsg)
			} else {
				e.StatusChan <- "No file to load!"
			}

		// The default keys for moving left inside the text area are the left arrow key, and Ctrl+B (move backward).
		case termbox.KeyArrowLeft, termbox.KeyCtrlB:
			e.MoveCursor(-1, 0)

		// The default keys for moving right inside the text area are the right arrow key, and Ctrl+F (move forward).
		case termbox.KeyArrowRight, termbox.KeyCtrlF:
			e.MoveCursor(1, 0)

		// The default keys for moving up inside the text area are the up arrow key, and Ctrl+P (move to previous line).
		case termbox.KeyArrowUp, termbox.KeyCtrlP:
			e.MoveCursor(0, -1)

		// The default keys for moving down inside the text area are the down arrow key, and Ctrl+N (move to next line).
		case termbox.KeyArrowDown, termbox.KeyCtrlN:
			e.MoveCursor(0, 1)

		// Home key, moves cursor to initial position (X=0).
		case termbox.KeyHome:
			e.SetX(0)

		// End key, moves cursor to final position (X= length of text).
		case termbox.KeyEnd:
			e.SetX(len(e.Text))

		// The default keys for deleting a character are Backspace and Delete.
		case termbox.KeyBackspace, termbox.KeyBackspace2:
			performOperation(OperationDelete, ev, conn)
		case termbox.KeyDelete:
			performOperation(OperationDelete, ev, conn)

		// The Tab key inserts 4 spaces to simulate a "tab".
		case termbox.KeyTab:
			for i := 0; i < 4; i++ {
				ev.Ch = ' '
				performOperation(OperationInsert, ev, conn)
			}

		// The Enter key inserts a newline character to the editor's content.
		case termbox.KeyEnter:
			ev.Ch = '\n'
			performOperation(OperationInsert, ev, conn)

		// The Space key inserts a space character to the editor's content.
		case termbox.KeySpace:
			ev.Ch = ' '
			performOperation(OperationInsert, ev, conn)

		// Every other key is eligible to be a candidate for insertion.
		default:
			if ev.Ch != 0 {
				performOperation(OperationInsert, ev, conn)
			}
		}
	}

	e.SendDraw()
	return nil
}

const (
	OperationInsert = iota
	OperationDelete
)

// performOperation performs a CRDT insert or delete operation on the local document and sends a message over the WebSocket connection.
func performOperation(opType int, ev termbox.Event, conn *websocket.Conn) {
	// Get position and value.
	ch := string(ev.Ch)

	var msg commons.Message

	// Modify local state (CRDT) first.
	switch opType {
	case OperationInsert:
		logger.Infof("LOCAL INSERT: %s at cursor position %v\n", ch, e.Cursor)

		text, err := doc.Insert(e.Cursor+1, ch)
		if err != nil {
			e.SetText(text)
			logger.Errorf("CRDT error: %v\n", err)
		}
		e.SetText(text)

		e.MoveCursor(1, 0)
		msg = commons.Message{Type: "operation", Operation: commons.Operation{Type: "insert", Position: e.Cursor, Value: ch}}

	case OperationDelete:
		logger.Infof("LOCAL DELETE: cursor position %v\n", e.Cursor)

		if e.Cursor-1 < 0 {
			e.Cursor = 0
		}

		text := doc.Delete(e.Cursor)
		e.SetText(text)

		msg = commons.Message{Type: "operation", Operation: commons.Operation{Type: "delete", Position: e.Cursor}}
		e.MoveCursor(-1, 0)
	}

	// Send the message.
	if e.IsConnected {
		err := conn.WriteJSON(msg)
		if err != nil {
			e.IsConnected = false
			e.StatusChan <- "lost connection!"
		}
	}
}

// getTermboxChan returns a channel of termbox Events repeatedly waiting on user input.
func getTermboxChan() chan termbox.Event {
	termboxChan := make(chan termbox.Event)

	go func() {
		for {
			termboxChan <- termbox.PollEvent()
		}
	}()

	return termboxChan
}

// handleMsg updates the CRDT document with the contents of the message.
func handleMsg(msg commons.Message, conn *websocket.Conn) {
	switch msg.Type {
	case commons.DocSyncMessage:
		logger.Infof("DOCSYNC RECEIVED, updating local doc %+v\n", msg.Document)
		doc = msg.Document
		e.SetText(crdt.Content(doc))

	case commons.DocReqMessage:
		logger.Infof("DOCREQ RECEIVED, sending local document to %v\n", msg.ID)
		docMsg := commons.Message{Type: commons.DocSyncMessage, Document: doc, ID: msg.ID}
		_ = conn.WriteJSON(&docMsg)

	case commons.SiteIDMessage:
		siteID, err := strconv.Atoi(msg.Text)
		if err != nil {
			logger.Errorf("failed to set siteID, err: %v\n", err)
		}
		crdt.SiteID = siteID
		logger.Infof("SITE ID %v, INTENDED SITE ID: %v", crdt.SiteID, siteID)

	case commons.JoinMessage:
		e.StatusChan <- fmt.Sprintf("%s has joined the session!", msg.Username)

	case commons.UsersMessage:
		e.StatusMu.Lock()
		e.Users = strings.Split(msg.Text, ",")
		e.StatusMu.Unlock()

	default:
		switch msg.Operation.Type {
		case "insert":
			_, err := doc.Insert(msg.Operation.Position, msg.Operation.Value)
			if err != nil {
				logger.Errorf("failed to insert, err: %v\n", err)
			}

			e.SetText(crdt.Content(doc))
			if msg.Operation.Position-1 <= e.Cursor {
				e.MoveCursor(len(msg.Operation.Value), 0)
			}
			logger.Infof("REMOTE INSERT: %s at position %v\n", msg.Operation.Value, msg.Operation.Position)

		case "delete":
			_ = doc.Delete(msg.Operation.Position)
			e.SetText(crdt.Content(doc))
			if msg.Operation.Position-1 <= e.Cursor {
				e.MoveCursor(-len(msg.Operation.Value), 0)
			}
			logger.Infof("REMOTE DELETE: position %v\n", msg.Operation.Position)
		}
	}

	printDoc(doc)
	e.SendDraw()
}

// getMsgChan returns a message channel that repeatedly reads from a websocket connection.
func getMsgChan(conn *websocket.Conn) chan commons.Message {
	messageChan := make(chan commons.Message)
	go func() {
		for {
			var msg commons.Message

			// Read message.
			err := conn.ReadJSON(&msg)
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					logger.Errorf("websocket error: %v", err)
				}
				e.IsConnected = false
				e.StatusChan <- "lost connection!"
				break
			}

			logger.Infof("message received: %+v\n", msg)

			// send message through channel
			messageChan <- msg

		}
	}()
	return messageChan
}

// handleStatusMsg asynchronously waits for messages from e.StatusChan and
// displays the message when it arrives.
func handleStatusMsg() {
	for msg := range e.StatusChan {
		e.StatusMu.Lock()
		e.StatusMsg = msg
		e.ShowMsg = true
		e.StatusMu.Unlock()

		logger.Infof("got status message: %s", e.StatusMsg)

		e.SendDraw()
		time.Sleep(6 * time.Second)

		e.StatusMu.Lock()
		e.ShowMsg = false
		e.StatusMu.Unlock()

		e.SendDraw()
	}

}

func drawLoop() {
	for {
		<-e.DrawChan
		e.Draw()
	}
}
