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
		return "", fmt.Errorf("usage: shelley login <anthropic|openai>")
	}
	switch args[0] {
	case "anthropic", "openai":
		return args[0], nil
	default:
		return "", fmt.Errorf("unsupported provider %q (supported: anthropic, openai)", args[0])
	}
}

// vendorName returns the human-readable vendor label for the ToS warning.
func vendorName(provider string) string {
	if provider == "anthropic" {
		return "Claude / Anthropic"
	}
	return "ChatGPT / OpenAI"
}

// printToSWarning warns the user that subscription auth in a non-official
// client is undocumented and may violate the provider's terms of service.
func printToSWarning(provider string) {
	fmt.Printf("WARNING: Using a %s subscription from a non-official client is\n", vendorName(provider))
	fmt.Println("undocumented and may violate the provider's terms of service. Proceed at")
	fmt.Println("your own risk.")
	fmt.Println()
}

// runLogin runs the interactive OAuth login flow for a subscription provider.
// OpenAI uses the Codex device-code flow (no localhost callback); Anthropic
// uses the paste-the-code browser flow.
func runLogin(args []string) {
	provider, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store := &oauth.Store{Path: oauth.DefaultCredentialsPath()}
	httpc := llmhttp.NewClient(nil)

	if provider == "openai" {
		runDeviceLogin(store, httpc)
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

// runDeviceLogin runs the OpenAI Codex device-code login: print a URL + short
// code, then poll until the user approves in a browser.
func runDeviceLogin(store *oauth.Store, httpc *http.Client) {
	printToSWarning("openai")
	flow := oauth.NewOpenAIDeviceFlow(store, httpc)

	// The device code expires server-side (typically 15 minutes); bound the
	// whole flow generously and let polling run until the user approves.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	da, err := flow.Start(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "device login failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Sign in with Device Code:")
	fmt.Println()
	fmt.Println("1. Open this link in your browser and sign in:")
	fmt.Println("   " + da.VerificationURL)
	fmt.Println()
	fmt.Println("2. Enter this one-time code (expires in ~15 minutes):")
	fmt.Println("   " + da.UserCode)
	fmt.Println()
	fmt.Println("Device codes are a common phishing target. Never share this code.")
	fmt.Println()
	fmt.Print("Waiting for authorization...")

	if err := flow.Poll(ctx, da); err != nil {
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
