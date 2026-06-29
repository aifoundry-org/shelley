package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/llm/oauth"
)

// parseLoginArgs validates the provider argument for `shelley login`.
// Only "anthropic" is supported today.
func parseLoginArgs(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: shelley login anthropic")
	}
	if args[0] != "anthropic" {
		return "", fmt.Errorf("unsupported provider %q (only \"anthropic\" is supported)", args[0])
	}
	return args[0], nil
}

// runLogin runs the interactive OAuth login flow for a subscription provider.
func runLogin(args []string) {
	if _, err := parseLoginArgs(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store := &oauth.Store{Path: oauth.DefaultCredentialsPath()}
	flow := oauth.NewAnthropicLoginFlow(store, llmhttp.NewClient(nil))

	fmt.Println("WARNING: Using a Claude subscription from a non-official client is")
	fmt.Println("undocumented and may violate Anthropic's terms of service. Proceed at")
	fmt.Println("your own risk.")
	fmt.Println()
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
