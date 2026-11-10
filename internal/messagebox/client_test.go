package messagebox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bsv-blockchain/go-bsv-middleware/pkg/middleware"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
)

func TestListAndAcknowledgeJSON(t *testing.T) {
	var acked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/listMessages":
			var req struct {
				MessageBox string `json:"messageBox"`
				Offset     int    `json:"offset"`
			}
			if err := json.Unmarshal(buf, &req); err != nil {
				t.Errorf("list body %s", buf)
			}
			if req.MessageBox != "payment_inbox" {
				t.Errorf("box %s", req.MessageBox)
			}
			body := `{"customInstructions":{"derivationPrefix":"YWI=","derivationSuffix":"Y2Q="},"transaction":[1],"amount":1}`
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"messages": []map[string]string{{
					"messageId": "m1",
					"sender":    "02aa",
					"body":      body,
				}},
				"hasMore":    false,
				"nextOffset": 1,
			})
		case "/acknowledgeMessage":
			var req struct {
				MessageIDs []string `json:"messageIds"`
			}
			_ = json.Unmarshal(buf, &req)
			acked = append(acked, req.MessageIDs...)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Box: "payment_inbox", Poster: HTTPPoster{Client: srv.Client()}}
	_, failed, err := c.ListPayments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The fixture transaction is not atomic beef, so parsing fails and the
	// message must stay unacknowledged.
	if len(failed) != 1 || failed[0].MessageID != "m1" {
		t.Fatalf("failed %+v", failed)
	}
	if err := c.Acknowledge(context.Background(), []string{"m1"}); err != nil {
		t.Fatal(err)
	}
	if len(acked) != 1 || acked[0] != "m1" {
		t.Fatalf("acked %v", acked)
	}
}

func TestAuthFetchReachesMessageBoxList(t *testing.T) {
	serverKey, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	serverWallet, err := sdk.NewCompletedProtoWallet(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	clientWallet, err := sdk.NewCompletedProtoWallet(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /listMessages", func(w http.ResponseWriter, r *http.Request) {
		id, err := middleware.ShouldGetAuthenticatedIdentity(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if id.ToDERHex() != clientKey.PubKey().ToDERHex() {
			t.Errorf("identity %s", id.ToDERHex())
		}
		var req struct {
			MessageBox string `json:"messageBox"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode %v", err)
		}
		if req.MessageBox != "payment_inbox" {
			t.Errorf("box %q", req.MessageBox)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "success",
			"messages":   []any{},
			"hasMore":    false,
			"nextOffset": 0,
		})
	})
	auth := middleware.NewAuth(serverWallet)
	srv := httptest.NewServer(auth.HTTPHandler(mux))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Box: "payment_inbox", Poster: NewAuthPoster(clientWallet)}
	got, failed, err := c.ListPayments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(failed) != 0 {
		t.Fatalf("payments %d failed %d", len(got), len(failed))
	}
}

func TestListPaymentsDecryptsEncryptedEnvelope(t *testing.T) {
	recipientKey, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	senderKey, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	recipientWallet, err := sdk.NewCompletedProtoWallet(recipientKey)
	if err != nil {
		t.Fatal(err)
	}
	senderWallet, err := sdk.NewCompletedProtoWallet(senderKey)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"customInstructions":{"derivationPrefix":"YWI=","derivationSuffix":"Y2Q="},"transaction":[1],"amount":1}`)
	enc, err := senderWallet.Encrypt(context.Background(), sdk.EncryptArgs{
		EncryptionArgs: sdk.EncryptionArgs{
			ProtocolID: sdk.Protocol{SecurityLevel: sdk.SecurityLevelEveryApp, Protocol: "messagebox"},
			KeyID:      "1",
			Counterparty: sdk.Counterparty{
				Type:         sdk.CounterpartyTypeOther,
				Counterparty: recipientKey.PubKey(),
			},
		},
		Plaintext: plain,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(map[string]string{"encryptedMessage": base64Encode(enc.Ciphertext)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"messages": []map[string]any{{
				"messageId": "enc1",
				"sender":    senderKey.PubKey().ToDERHex(),
				"body":      json.RawMessage(envelope),
			}},
			"hasMore": false,
		})
	}))
	defer srv.Close()
	c := &Client{
		BaseURL: srv.URL,
		Box:     "payment_inbox",
		Poster:  HTTPPoster{Client: srv.Client()},
		Wallet:  recipientWallet,
	}
	_, failed, err := c.ListPayments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].MessageID != "enc1" {
		t.Fatalf("failed %+v", failed)
	}
	// After decrypt, this fixture is a token with a non-beef transaction.
	if failed[0].Err == nil || failed[0].Err.Error() == "message body is not a PeerPay payment token" {
		t.Fatalf("wanted a post-decrypt parse error, got %v", failed[0].Err)
	}
}

func base64Encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func TestListPaymentsUnwrapsStoredMessageEnvelope(t *testing.T) {
	recipientKey, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	senderKey, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	recipientWallet, err := sdk.NewCompletedProtoWallet(recipientKey)
	if err != nil {
		t.Fatal(err)
	}
	senderWallet, err := sdk.NewCompletedProtoWallet(senderKey)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"customInstructions":{"derivationPrefix":"YWI=","derivationSuffix":"Y2Q="},"transaction":[1],"amount":1}`)
	enc, err := senderWallet.Encrypt(context.Background(), sdk.EncryptArgs{
		EncryptionArgs: sdk.EncryptionArgs{
			ProtocolID: sdk.Protocol{SecurityLevel: sdk.SecurityLevelEveryApp, Protocol: "messagebox"},
			KeyID:      "1",
			Counterparty: sdk.Counterparty{
				Type:         sdk.CounterpartyTypeOther,
				Counterparty: recipientKey.PubKey(),
			},
		},
		Plaintext: plain,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := json.Marshal(map[string]string{"encryptedMessage": base64Encode(enc.Ciphertext)})
	if err != nil {
		t.Fatal(err)
	}
	// Live go-message-box-server stores body as a JSON string of
	// {"message": "<json string of {encryptedMessage}>"}.
	wrapped, err := json.Marshal(map[string]string{"message": string(inner)})
	if err != nil {
		t.Fatal(err)
	}
	asString, err := json.Marshal(string(wrapped))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"messages": []map[string]any{{
				"messageId": "enc2",
				"sender":    senderKey.PubKey().ToDERHex(),
				"body":      json.RawMessage(asString),
			}},
			"hasMore": false,
		})
	}))
	defer srv.Close()
	c := &Client{
		BaseURL: srv.URL,
		Box:     "payment_inbox",
		Poster:  HTTPPoster{Client: srv.Client()},
		Wallet:  recipientWallet,
	}
	_, failed, err := c.ListPayments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].MessageID != "enc2" {
		t.Fatalf("failed %+v", failed)
	}
	if failed[0].Err == nil || failed[0].Err.Error() == "message body is not a PeerPay payment token" {
		t.Fatalf("wanted a post-decrypt parse error, got %v", failed[0].Err)
	}
}
