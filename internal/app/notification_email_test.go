package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"
)

type smtpCapture struct {
	commands []string
	message  string
	err      error
}

func notificationSMTPServer(t *testing.T, mode string, serverTLS *tls.Config) (string, int, <-chan smtpCapture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if mode == "tls" {
		listener = tls.NewListener(listener, serverTLS)
	}
	t.Cleanup(func() { listener.Close() })
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	done := make(chan smtpCapture, 1)
	go func() {
		result := smtpCapture{}
		defer func() { done <- result }()
		conn, err := listener.Accept()
		if err != nil {
			result.err = err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader, writer := bufio.NewReader(conn), bufio.NewWriter(conn)
		reply := func(message string) { fmt.Fprint(writer, message+"\r\n"); writer.Flush() }
		reply("220 localhost ESMTP")
		secure := mode == "tls"
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					result.err = err
				}
				return
			}
			line = strings.TrimSpace(line)
			result.commands = append(result.commands, line)
			switch {
			case strings.HasPrefix(line, "EHLO"):
				if !secure && mode == "starttls" {
					reply("250-localhost\r\n250-AUTH PLAIN\r\n250 STARTTLS")
				} else {
					reply("250-localhost\r\n250 AUTH PLAIN")
				}
			case line == "STARTTLS":
				if mode != "starttls" {
					reply("500 STARTTLS unavailable")
					continue
				}
				reply("220 Ready for TLS")
				secured := tls.Server(conn, serverTLS)
				if err := secured.Handshake(); err != nil {
					result.err = err
					return
				}
				conn, secure = secured, true
				reader, writer = bufio.NewReader(conn), bufio.NewWriter(conn)
			case strings.HasPrefix(line, "AUTH "):
				if !secure {
					reply("535 Authentication requires TLS")
				} else {
					reply("235 Authenticated")
				}
			case strings.HasPrefix(line, "MAIL FROM:") || strings.HasPrefix(line, "RCPT TO:"):
				reply("250 Accepted")
			case line == "DATA":
				reply("354 End with dot")
				var message strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						result.err = err
						return
					}
					if line == ".\r\n" {
						break
					}
					message.WriteString(line)
				}
				result.message = message.String()
				reply("250 Queued")
			case line == "QUIT":
				reply("221 Bye")
				return
			default:
				reply("500 Unexpected command")
			}
		}
	}()
	return host, number, done
}

func TestNotificationEmailTLSAndSTARTTLS(t *testing.T) {
	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate, cert := certServer.TLS.Certificates[0], certServer.Certificate()
	certServer.Close()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			host, port, done := notificationSMTPServer(t, mode, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
			channel := NotificationChannel{SMTPHost: host, SMTPPort: port, SMTPTLS: mode, SMTPUsername: "notify@example.com", SMTPFrom: "LedgerGate <notify@example.com>", SMTPTo: []string{"one@example.com", "two@example.com"}}
			alert := Alert{Title: "余额提醒\r\nBcc: 注入无效", Detail: "余额已低于阈值\n请及时充值。"}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := sendSMTPNotification(ctx, channel, alert, "email-password", &tls.Config{ServerName: host, RootCAs: pool, MinVersion: tls.VersionTLS12}); err != nil {
				t.Fatal(err)
			}
			capture := <-done
			if capture.err != nil {
				t.Fatal(capture.err)
			}
			message, err := mail.ReadMessage(strings.NewReader(capture.message))
			if err != nil {
				t.Fatal(err)
			}
			subject, err := (&mime.WordDecoder{}).DecodeHeader(message.Header.Get("Subject"))
			if err != nil || subject != "余额提醒  Bcc: 注入无效" || message.Header.Get("Bcc") != "" {
				t.Fatal("email subject encoding or header injection protection failed", subject, err)
			}
			body, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
			if string(body) != alert.Detail {
				t.Fatal("email body encoding failed", string(body))
			}
			commands := strings.Join(capture.commands, "\n")
			auth := "AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00notify@example.com\x00email-password"))
			if !strings.Contains(commands, auth) || !strings.Contains(commands, "RCPT TO:<two@example.com>") || mode == "starttls" && strings.Index(commands, "AUTH") < strings.Index(commands, "STARTTLS") {
				t.Fatal("SMTP authentication or recipient flow incorrect")
			}
		})
	}
}

func TestNotificationEmailFailsClosedWithoutSTARTTLS(t *testing.T) {
	a := testApp(t)
	host, port, done := notificationSMTPServer(t, "plain", nil)
	channel := NotificationChannel{Name: "Email", Type: "email", Retries: 2, SMTPHost: host, SMTPPort: port, SMTPTLS: "starttls", SMTPUsername: "notify@example.com", SMTPPassword: a.seal("private-password"), SMTPFrom: "notify@example.com", SMTPTo: []string{"receiver@example.com"}}
	if err := a.sendNotification(context.Background(), channel, Alert{Title: "测试"}); err == nil {
		t.Fatal("SMTP fell back to unencrypted connection")
	}
	capture := <-done
	for _, command := range capture.commands {
		if strings.HasPrefix(command, "AUTH") || strings.HasPrefix(command, "MAIL") {
			t.Fatal("sent credentials or mail without TLS")
		}
	}
}
