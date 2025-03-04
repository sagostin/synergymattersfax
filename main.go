package main

/*

	- run simple ftp server with mapped paths to local storage
	- connect to pop3 email server to fetch content / attachments n such
	- send webhook request to secondary service on say fax relay
	-

*/

import (
	"fmt"
	"github.com/joho/godotenv"
	"github.com/knadh/go-pop3"
	"log"
	"os"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		return
	}
	go startFtp()
	go watchFaxFolder(os.Getenv("FTP_ROOT") + FaxDir)
	// go monitorDoneFiles(os.Getenv("FTP_ROOT") + FaxDir)
	// go monitorStatusFiles(os.Getenv("FTP_ROOT") + FaxDir)

	//go startPop3()

	select {} // Block forever
}

func startPop3() {
	// Initialize the client.
	p := pop3.New(pop3.Opt{
		Host:          "pop.gmail.com",
		Port:          995,
		TLSEnabled:    true,
		TLSSkipVerify: true,
	})

	// Create a new connection. POP3 connections are stateful and should end
	// with a Quit() once the opreations are done.
	c, err := p.NewConn()
	if err != nil {
		log.Fatal(err)
	}
	defer c.Quit()

	// Authenticate.
	if err := c.Auth("", ""); err != nil {
		log.Fatal(err)
	}

	// Print the total number of messages and their size.
	count, size, _ := c.Stat()
	fmt.Println("total messages=", count, "size=", size)

	// Pull the list of all message IDs and their sizes.
	msgs, _ := c.List(0)
	for _, m := range msgs {
		fmt.Println("id=", m.ID, "size=", m.Size)
	}

	// Pull all messages on the server. Message IDs go from 1 to N.
	for id := 1; id <= count; id++ {
		m, _ := c.Retr(id)

		fmt.Println(id, "=", m.Header.Get("subject"))

		// To read the multi-part e-mail bodies, see:
		// https://github.com/emersion/go-message/blob/master/example_test.go#L12
	}

	// Delete all the messages. Server only executes deletions after a successful Quit()
	for id := 1; id <= count; id++ {
		c.Dele(id)
	}
}
