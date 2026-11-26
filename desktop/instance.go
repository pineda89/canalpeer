package main

import (
	"bufio"
	"errors"
	"strings"
	"time"
)

// claimInstance makes this process the one window: whoever listens on path
// first gets every link later instances send. A second instance gets
// primary=false and should hand its link over with sendToPrimary and exit.
// Listening comes first so two instances starting together cannot both win.
func claimInstance(path string) (links <-chan string, primary bool, err error) {
	for attempt := 0; attempt < 3; attempt++ {
		ln, err := listenInstance(path)
		if err == nil {
			ch := make(chan string, 8)
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					c.SetReadDeadline(time.Now().Add(2 * time.Second))
					line, _ := bufio.NewReader(c).ReadString('\n')
					c.Close()
					if l := strings.TrimSpace(line); l != "" {
						ch <- l
					}
				}
			}()
			return ch, true, nil
		}
		// The winner may be between creating its socket and listening on it:
		// give it a moment before calling the socket stale.
		for try := 0; try < 5; try++ {
			if c, derr := dialInstance(path); derr == nil {
				c.Close()
				return nil, false, nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		removeStaleInstance(path) // nobody answers: a crashed instance left it behind
	}
	return nil, false, errors.New("cannot claim the instance channel")
}

func sendToPrimary(path, link string) error {
	if strings.ContainsAny(link, "\r\n") {
		return errors.New("link with a line break")
	}
	c, err := dialInstance(path)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte(link + "\n"))
	return err
}
