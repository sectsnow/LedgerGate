package app

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

func (a *App) sendNotificationEmail(ctx context.Context, channel NotificationChannel, alert Alert) error {
	if err := a.validateNotification(channel, true); err != nil {
		return err
	}
	password, err := a.unseal(channel.SMTPPassword)
	if err != nil {
		return err
	}
	return sendSMTPNotification(ctx, channel, alert, password, &tls.Config{ServerName: channel.SMTPHost, MinVersion: tls.VersionTLS12})
}

func sendSMTPNotification(ctx context.Context, channel NotificationChannel, alert Alert, password string, config *tls.Config) error {
	address := net.JoinHostPort(channel.SMTPHost, strconv.Itoa(channel.SMTPPort))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if channel.SMTPTLS == "tls" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: config}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return errors.New("SMTP 连接或 TLS 握手失败，请检查主机、端口和证书")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return errors.New("设置 SMTP 超时失败")
		}
	}
	client, err := smtp.NewClient(conn, channel.SMTPHost)
	if err != nil {
		return errors.New("SMTP 握手失败")
	}
	defer client.Close()
	if channel.SMTPTLS == "starttls" {
		if err := client.StartTLS(config); err != nil {
			return errors.New("SMTP STARTTLS 失败，未发送认证凭据")
		}
	}
	if channel.SMTPUsername != "" {
		if err := client.Auth(smtp.PlainAuth("", channel.SMTPUsername, password, channel.SMTPHost)); err != nil {
			return errors.New("SMTP 认证失败，请检查用户名和邮箱授权码")
		}
	}
	from, _ := mail.ParseAddress(channel.SMTPFrom)
	if err := client.Mail(from.Address); err != nil {
		return errors.New("SMTP 拒绝发件人")
	}
	to := make([]string, 0, len(channel.SMTPTo))
	for _, address := range channel.SMTPTo {
		recipient, _ := mail.ParseAddress(address)
		if err := client.Rcpt(recipient.Address); err != nil {
			return errors.New("SMTP 拒绝收件人")
		}
		to = append(to, recipient.String())
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP 拒绝接收邮件正文")
	}
	subject := mime.QEncoding.Encode("UTF-8", strings.NewReplacer("\r", " ", "\n", " ").Replace(alert.Title))
	body := base64.StdEncoding.EncodeToString([]byte(describeAlert(alert).Detail))
	lines := []string{}
	for len(body) > 76 {
		lines, body = append(lines, body[:76]), body[76:]
	}
	lines = append(lines, body)
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n", from.String(), strings.Join(to, ", "), subject, time.Now().Format(time.RFC1123Z), strings.Join(lines, "\r\n"))
	if _, err := writer.Write([]byte(message)); err != nil {
		return errors.New("SMTP 正文发送失败")
	}
	if err := writer.Close(); err != nil {
		return errors.New("SMTP 未确认接收邮件")
	}
	// DATA 已确认接收，QUIT 失败也不重发，避免重复邮件。
	_ = client.Quit()
	return nil
}
