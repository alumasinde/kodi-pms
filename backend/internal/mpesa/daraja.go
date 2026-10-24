package mpesa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kodipms/internal/platform/cache"
)

// Daraja is the whole of Safaricom this module asks of the network: push a prompt
// to a phone. Receipts arrive the other way, as callbacks, and reading a
// confirmation needs no client at all. Keeping the interface this small is what
// lets the reconciliation and STK paths be tested without a sandbox account.
type Daraja interface {
	STKPush(ctx context.Context, environment string, creds Credentials, req STKPushRequest) (STKPushReply, error)
}

// Credentials is one shortcode's decrypted Daraja key pair and its passkey. It
// exists only as a call argument: never in a DTO, never in a log line.
type Credentials struct {
	ConsumerKey    string
	ConsumerSecret string
	Passkey        string
}

type STKPushRequest struct {
	Shortcode   string
	AccountType string
	AccountRef  string
	Phone       string
	Amount      float64
	Description string
	MerchantID  string
	CheckoutID  string
	CallbackURL string
}

// STKPushReply is what Daraja answers the HTTP call itself, before the tenant has
// done anything. ResultCode 0 means the prompt was sent, not that it was paid.
type STKPushReply struct {
	MerchantID      string
	CheckoutID      string
	ResultCode      int
	ResultDesc      string
	CustomerMessage string
}

type darajaClient struct {
	store      cache.Store
	httpClient *http.Client
	log        *slog.Logger
	baseURLs   map[string]string
}

// NewDaraja builds the live client. The sandbox and production hosts are fixed
// here rather than in configuration: they are Safaricom's, and an operator who
// points a production shortcode at a proxy is configuring a leak.
func NewDaraja(store cache.Store, log *slog.Logger) Daraja {
	return &darajaClient{
		store:    store,
		log:      log,
		baseURLs: map[string]string{envSandbox: "https://sandbox.safaricom.co.ke", envProduction: "https://api.safaricom.co.ke"},
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    8,
				IdleConnTimeout: 30 * time.Second,
			},
		},
	}
}

var errNoDaraja = errors.New("the M-Pesa gateway is not configured")

// disabledGateway is what NewService uses when no client was wired, so a
// deployment that has not set up Daraja keys can still run and every prompt
// fails with an explanation instead of a nil pointer.
type disabledGateway struct{}

func (disabledGateway) STKPush(context.Context, string, Credentials, STKPushRequest) (STKPushReply, error) {
	return STKPushReply{}, errNoDaraja
}

func (c *darajaClient) base(environment string) (string, error) {
	url, ok := c.baseURLs[environment]
	if !ok {
		return "", fmt.Errorf("unknown M-Pesa environment %q", environment)
	}
	return url, nil
}

// token returns a cached Bearer token for this consumer key. Safaricom's own
// expiry is 3599 seconds and the cache holds one for 25 minutes, which is the
// margin that keeps a prompt from being sent with a token that expired while it
// was in flight.
func (c *darajaClient) token(ctx context.Context, environment string, creds Credentials) (string, error) {
	base, err := c.base(environment)
	if err != nil {
		return "", err
	}
	key := oauthCacheKey(environment, creds.ConsumerKey)
	if cached, err := c.store.Get(ctx, key); err == nil && cached != "" {
		return cached, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/oauth/v1/generate?grant_type=client_credentials", nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(creds.ConsumerKey, creds.ConsumerSecret)
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("daraja oauth: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, res.Body); _ = res.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("daraja oauth: status %d", res.StatusCode)
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   Scalar `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("daraja oauth: unreadable response")
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("daraja oauth: no access token in response")
	}
	ttl := oauthTTL
	if seconds, err := strconv.ParseInt(parsed.ExpiresIn.String(), 10, 64); err == nil && seconds > 120 {
		if time.Duration(seconds)*time.Second < oauthTTL {
			ttl = time.Duration(seconds-60) * time.Second
		}
	}
	if err := c.store.Set(ctx, key, parsed.AccessToken, ttl); err != nil {
		// A cache that will not take the token is not a reason to fail a prompt
		// this request already paid for.
		c.log.Warn("cache daraja token", "environment", environment, "error", err)
	}
	return parsed.AccessToken, nil
}

// oauthCacheKey names a token by the environment and a hash of the consumer key,
// so two organizations with the same shortcode in different environments cannot
// share one token and the key itself never appears in a Redis key listing.
func oauthCacheKey(environment, consumerKey string) string {
	sum := sha256.Sum256([]byte(consumerKey))
	return "mpesa:oauth:" + environment + ":" + hex.EncodeToString(sum[:8])
}

// stkPassword is Daraja's BusinessPassword: the shortcode, the passkey and the
// timestamp of this call, base64'd together. The timestamp layout is theirs, not
// ISO, and the whole value is only valid for a minute.
func stkPassword(shortcode, passkey string, at time.Time) string {
	raw := shortcode + passkey + at.Format(darajaTimeLayout)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

func (c *darajaClient) STKPush(ctx context.Context, environment string, creds Credentials, req STKPushRequest) (STKPushReply, error) {
	base, err := c.base(environment)
	if err != nil {
		return STKPushReply{}, err
	}
	token, err := c.token(ctx, environment, creds)
	if err != nil {
		return STKPushReply{}, err
	}

	transactionType := "CustomerPayBillOnline"
	if req.AccountType == typeTill {
		transactionType = "CustomerBuyGoodsOnline"
	}
	body := map[string]any{
		"BusinessShortCode": req.Shortcode,
		"Password":          stkPassword(req.Shortcode, creds.Passkey, time.Now()),
		"Timestamp":         time.Now().Format(darajaTimeLayout),
		"TransactionType":   transactionType,
		"Amount":            req.Amount,
		"PhoneNumber":       req.Phone,
		"CallBackURL":       req.CallbackURL,
		"AccountReference":  req.AccountRef,
		"MerchantRequestID": req.MerchantID,
		"CheckoutRequestID": req.CheckoutID,
	}
	if req.Description != "" {
		body["Description"] = req.Description
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return STKPushReply{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/mpesa/stkpush/v1/processrequest", bytes.NewReader(raw))
	if err != nil {
		return STKPushReply{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		return STKPushReply{}, fmt.Errorf("daraja stk push: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, res.Body); _ = res.Body.Close() }()

	resBody, err := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if err != nil {
		return STKPushReply{}, err
	}
	var parsed struct {
		MerchantRequestID string `json:"MerchantRequestID"`
		CheckoutRequestID string `json:"CheckoutRequestID"`
		ResultCode        Scalar `json:"ResultCode"`
		ResultDesc        string `json:"ResultDesc"`
		CustomerMessage   string `json:"CustomerMessage"`
	}
	// A gateway that answers something other than JSON is a gateway failure, not a
	// rejected prompt: the difference decides whether the operator retries.
	if err := json.Unmarshal(resBody, &parsed); err != nil {
		return STKPushReply{}, fmt.Errorf("daraja stk push: status %d, unreadable response", res.StatusCode)
	}
	code, _ := strconv.Atoi(strings.TrimSpace(parsed.ResultCode.String()))
	out := STKPushReply{
		MerchantID:      strings.TrimSpace(parsed.MerchantRequestID),
		CheckoutID:      strings.TrimSpace(parsed.CheckoutRequestID),
		ResultCode:      code,
		ResultDesc:      truncate(parsed.ResultDesc, maxResultDesc),
		CustomerMessage: truncate(parsed.CustomerMessage, maxDescription),
	}
	if res.StatusCode != http.StatusOK && code == 0 {
		return out, fmt.Errorf("daraja stk push: status %d", res.StatusCode)
	}
	return out, nil
}
