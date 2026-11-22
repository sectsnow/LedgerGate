package app

import (
	"fmt"
	"strings"
	"testing"

	"ledgergate/internal/backup"
)

func TestNotificationTransferCredentialsRedactionAndLegacyPackages(t *testing.T) {
	source, _ := transferFixture(t)
	for _, row := range []NotificationChannel{
		{Name: "Telegram", Type: "telegram", Enabled: true, Token: source.seal("123:abcdefghijklmno"), ChatID: "-100", Retries: 4},
		{Name: "Email", Type: "email", Enabled: true, SMTPHost: "smtp.example.com", SMTPPort: 587, SMTPTLS: "starttls", SMTPUsername: "notify@example.com", SMTPPassword: source.seal("mail-private-password"), SMTPFrom: "notify@example.com", SMTPTo: []string{"receive@example.com"}, Retries: 3},
		{Name: "Server酱", Type: "serverchan", Enabled: true, SendKey: source.seal("sctp123tabcdef"), Retries: 2},
	} {
		if err := source.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, credentials := range []bool{true, false} {
		t.Run(fmt.Sprint(credentials), func(t *testing.T) {
			raw, doc := exportTransferForTest(t, source, []string{"settings"}, credentials)
			if doc.Settings.NotificationChannels == nil || len(*doc.Settings.NotificationChannels) != 4 {
				t.Fatal("notification channels missing from settings export")
			}
			plain := jsonString(doc)
			for _, secret := range []string{"https://notify.invalid/hook", "notification-secret", "123:abcdefghijklmno", "mail-private-password", "sctp123tabcdef"} {
				if strings.Contains(plain, secret) != credentials {
					t.Fatal("notification credential flag ignored")
				}
			}
			target := testApp(t)
			h := target.Router()
			cookie := loginForTest(t, h)
			preview := transferPreviewForTest(t, h, cookie, raw, nil)
			response := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			var channels []NotificationChannel
			target.DB.Order("id").Find(&channels)
			if len(channels) != 4 {
				t.Fatal("notification channels not imported")
			}
			for _, channel := range channels {
				if channel.Enabled {
					t.Fatal("import enabled notification before review")
				}
				value := channel.URL + channel.Secret + channel.Token + channel.SendKey + channel.SMTPPassword
				if (value != "") != credentials {
					t.Fatal("credential redaction not preserved on import")
				}
				if channel.Type == "email" && credentials {
					if password, err := target.unseal(channel.SMTPPassword); err != nil || password != "mail-private-password" {
						t.Fatal("SMTP password not reencrypted", err)
					}
				}
			}
		})
	}
	_, doc := exportTransferForTest(t, source, []string{"settings"}, true)
	doc.Settings.NotificationChannels = nil
	doc.Settings.WebhookURL, doc.Settings.WebhookSigningKey = "https://legacy.invalid/hook", "legacy-private-secret"
	raw, err := backup.EncryptData([]byte(jsonString(doc)), transferTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	target := testApp(t)
	h := target.Router()
	cookie := loginForTest(t, h)
	preview := transferPreviewForTest(t, h, cookie, raw, nil)
	response := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var channels []NotificationChannel
	target.DB.Find(&channels)
	if len(channels) != 1 || channels[0].Enabled || channels[0].Name != "原通知渠道" {
		t.Fatal("legacy package not converted to disabled channel")
	}
	if secret, err := target.unseal(channels[0].Secret); err != nil || secret != "legacy-private-secret" {
		t.Fatal("legacy credential lost")
	}
}

func TestNotificationTransferPreviewDetectsCredentialChanges(t *testing.T) {
	source, _ := transferFixture(t)
	raw, _ := exportTransferForTest(t, source, []string{"settings"}, true)
	target := testApp(t)
	h := target.Router()
	cookie := loginForTest(t, h)
	channel := NotificationChannel{Name: "current", Type: "webhook", URL: target.seal("https://current.invalid"), Retries: 2}
	if err := target.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	preview := transferPreviewForTest(t, h, cookie, raw, nil)
	if err := target.DB.Model(&channel).UpdateColumn("secret", target.seal("changed-after-preview")).Error; err != nil {
		t.Fatal(err)
	}
	before := transferDBState(t, target)
	response := transferUploadForTest(t, h, cookie, "/api/data-transfer/import", raw, transferTestPassword, preview.Selected, preview.Token)
	if response.Code != 409 || transferDBState(t, target) != before {
		t.Fatal("notification edit did not invalidate import preview", response.Code, response.Body.String())
	}
}
