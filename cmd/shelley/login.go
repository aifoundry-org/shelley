package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/llm/oauth"
)

// parseLoginArgs validates the provider argument for `shelley login`.
func parseLoginArgs(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: shelley login <anthropic|openai|kimi>")
	}
	switch args[0] {
	case "anthropic", "openai", "kimi":
		return args[0], nil
	default:
		return "", fmt.Errorf("unsupported provider %q (supported: anthropic, openai, kimi)", args[0])
	}
}

// vendorName returns the human-readable vendor label for the ToS warning.
func vendorName(provider string) string {
	if provider == "anthropic" {
		return "Claude / Anthropic"
	}
	if provider == "kimi" {
		return "Kimi Code"
	}
	return "ChatGPT / OpenAI"
}

// printToSWarning warns the user that subscription auth in a non-official
// client is undocumented and may violate the provider's terms of service.
func printToSWarning(provider string) {
	if provider == "kimi" {
		fmt.Println("Kimi Code subscription sign-in uses a third-party OAuth client integration.")
		fmt.Println("Use an eligible account and comply with Kimi’s terms and usage limits.")
		fmt.Println()
		return
	}
	fmt.Printf("WARNING: Using a %s subscription from a non-official client is\n", vendorName(provider))
	fmt.Println("undocumented and may violate the provider's terms of service. Proceed at")
	fmt.Println("your own risk.")
	fmt.Println()
}

// runLogin runs the interactive OAuth login flow for a subscription provider.
// OpenAI and Kimi use device-code flows (no localhost callback); Anthropic
// uses the paste-the-code browser flow.
func runLogin(args []string) {
	provider, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store := &oauth.Store{Path: oauth.DefaultCredentialsPath()}
	httpc := llmhttp.NewClient(nil)

	if provider == "openai" || provider == "kimi" {
		runDeviceLogin(provider, store, httpc)
		return
	}

	printToSWarning(provider)
	flow := oauth.NewAnthropicLoginFlow(store, httpc)
	fmt.Println("1. Open this URL in your browser and approve access:")
	fmt.Println()
	fmt.Println("   " + flow.AuthorizeURL())
	fmt.Println()
	fmt.Print("2. Paste the authorization code shown after approval: ")

	reader := bufio.NewReader(os.Stdin)
	code, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "read code: %v\n", err)
		os.Exit(1)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		fmt.Fprintln(os.Stderr, "no code provided")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := flow.Complete(ctx, code); err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Logged in. Credentials saved to " + store.Path)
}

// runDeviceLogin runs device-code login: print a URL + short
// code, then poll until the user approves in a browser.
func runDeviceLogin(provider string, store *oauth.Store, httpc *http.Client) {
	printToSWarning(provider)

	// The device code expires server-side (typically 15 minutes); bound the
	// whole flow generously and let polling run until the user approves.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var verificationURL, userCode string
	var poll func(context.Context) error
	var err error
	switch provider {
	case "openai":
		flow := oauth.NewOpenAIDeviceFlow(store, httpc)
		var da *oauth.DeviceAuth
		da, err = flow.Start(ctx)
		if err == nil {
			verificationURL, userCode = da.VerificationURL, da.UserCode
			poll = func(ctx context.Context) error { return flow.Poll(ctx, da) }
		}
	case "kimi":
		flow := oauth.NewKimiDeviceFlow(store, httpc)
		var da *oauth.KimiDeviceAuth
		da, err = flow.Start(ctx)
		if err == nil {
			verificationURL, userCode = da.VerificationURL, da.UserCode
			poll = func(ctx context.Context) error { return flow.Poll(ctx, da) }
		}
	default:
		err = fmt.Errorf("unsupported device login provider %q", provider)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "device login failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Sign in with Device Code:")
	fmt.Println()
	fmt.Println("1. Open this link in your browser and sign in:")
	fmt.Println("   " + verificationURL)
	fmt.Println()
	fmt.Println("2. If prompted, enter this one-time code before it expires:")
	fmt.Println("   " + userCode)
	fmt.Println()
	fmt.Println("Device codes are a common phishing target. Never share this code.")
	fmt.Println()
	fmt.Print("Waiting for authorization...")

	if err := poll(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "\ndevice login failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(" done.")
	fmt.Println("Logged in. Credentials saved to " + store.Path)
}

// runLogout removes stored subscription credentials for a provider.
func runLogout(args []string) {
	provider, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store := &oauth.Store{Path: oauth.DefaultCredentialsPath()}
	if err := store.Delete(provider); err != nil {
		fmt.Fprintf(os.Stderr, "logout failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Logged out of " + provider + " subscription.")
}

// runLoginStatus prints subscription login status.
func runLoginStatus(args []string) {
	provider, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store := &oauth.Store{Path: oauth.DefaultCredentialsPath()}
	fmt.Printf("%s: %s\n", provider, oauth.Status(store, provider, time.Now()))
}
