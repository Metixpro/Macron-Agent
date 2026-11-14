package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/crypto/ssh"
)

func main() {
	env := strings.Split(os.Getenv("REMOTE_ENV"), "|")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	fmt.Println(ssh.FingerprintSHA256(signer.PublicKey()))
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if string(pw) == "pw" {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:2222")
	if err != nil {
		log.Fatal(err)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			_, chans, reqs, err := ssh.NewServerConn(c, cfg)
			if err != nil {
				c.Close()
				return
			}
			go func() {
				for r := range reqs {
					if r.WantReply {
						r.Reply(true, nil)
					}
				}
			}()
			for nc := range chans {
				switch nc.ChannelType() {
				case "direct-tcpip":
					var p struct {
						Host  string
						Port  uint32
						OHost string
						OPort uint32
					}
					ssh.Unmarshal(nc.ExtraData(), &p)
					tc, err := net.Dial("tcp", net.JoinHostPort(p.Host, fmt.Sprint(p.Port)))
					if err != nil {
						nc.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					ch, rq, _ := nc.Accept()
					go ssh.DiscardRequests(rq)
					go func() { io.Copy(ch, tc); ch.CloseWrite() }()
					go func() { io.Copy(tc, ch); tc.Close() }()
				case "session":
					ch, rq, _ := nc.Accept()
					go func() {
						for r := range rq {
							if r.Type != "exec" {
								r.Reply(false, nil)
								continue
							}
							var p struct{ Cmd string }
							ssh.Unmarshal(r.Payload, &p)
							r.Reply(true, nil)
							cmd := exec.Command("sh", "-c", p.Cmd)
							cmd.Env = append(os.Environ(), env...)
							cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
							in, _ := cmd.StdinPipe()
							go func() { io.Copy(in, ch); in.Close() }()
							go func() {
								cmd.Run()
								ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{0}))
								ch.Close()
							}()
						}
					}()
				}
			}
		}()
	}
}
