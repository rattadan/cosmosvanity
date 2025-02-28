package main

import (
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/tendermint/tendermint/crypto/secp256k1"
)

type matcher struct {
	StartsWith string
	EndsWith   string
	Contains   string
	Letters    int
	Digits     int
}

func (m matcher) Match(candidate string) bool {
	candidate = strings.TrimPrefix(candidate, "cosmos1")
	if !strings.HasPrefix(candidate, m.StartsWith) {
		return false
	}
	if !strings.HasSuffix(candidate, m.EndsWith) {
		return false
	}
	if !strings.Contains(candidate, m.Contains) {
		return false
	}
	if countUnionChars(candidate, bech32digits) < m.Digits {
		return false
	}
	if countUnionChars(candidate, bech32letters) < m.Letters {
		return false
	}
	return true
}

func (m matcher) ValidationErrors() []string {
	var errs []string
	if !bech32Only(m.Contains) || !bech32Only(m.StartsWith) || !bech32Only(m.EndsWith) {
		errs = append(errs, "ERROR: A provided matcher contains bech32 incompatible characters")
	}
	if len(m.Contains) > 38 || len(m.StartsWith) > 38 || len(m.EndsWith) > 38 {
		errs = append(errs, "ERROR: A provided matcher is too long. Must be max 38 characters.")
	}
	if m.Digits < 0 || m.Letters < 0 {
		errs = append(errs, "ERROR: Can't require negative amount of characters")
	}
	if m.Digits+m.Letters > 38 {
		errs = append(errs, "ERROR: Can't require more than 38 characters")
	}
	return errs
}

type wallet struct {
	Address string
	Pubkey  []byte
	Privkey []byte
}

func (w wallet) String() string {
	return "Address:\t" + w.Address + "\n" +
		"Public key:\t" + hex.EncodeToString(w.Pubkey) + "\n" +
		"Private key:\t" + hex.EncodeToString(w.Privkey)
}

func generateWallet() wallet {
	var privkey secp256k1.PrivKey = secp256k1.GenPrivKey()
	var pubkey secp256k1.PubKey = privkey.PubKey().(secp256k1.PubKey)
	bech32Addr, err := bech32.ConvertAndEncode("cosmos", pubkey.Address())
	if err != nil {
		panic(err)
	}

	return wallet{bech32Addr, pubkey, privkey}
}

func findMatchingWallets(ch chan wallet, quit chan struct{}, m matcher, attempts *uint64) {
	for {
		select {
		case <-quit:
			return
		default:
			w := generateWallet()
			atomic.AddUint64(attempts, 1)
			if m.Match(w.Address) {
				select {
				case ch <- w:
				default:
				}
			}
		}
	}
}

func (m matcher) calculateExpectedAttempts() float64 {
	// Base probability space for bech32 characters
	const addressLength = 38 // Standard cosmos address length after prefix
	
	// Start with base probability
	probability := 1.0
	
	// Calculate probability for StartsWith
	if len(m.StartsWith) > 0 {
		// Each position has 1/32 chance for exact match
		probability *= math.Pow(1.0/32.0, float64(len(m.StartsWith)))
	}
	
	// Calculate probability for EndsWith
	if len(m.EndsWith) > 0 {
		// Each position has 1/32 chance for exact match
		probability *= math.Pow(1.0/32.0, float64(len(m.EndsWith)))
	}
	
	// Calculate probability for Contains
	if len(m.Contains) > 0 {
		// For contains, we have multiple possible positions
		// Probability is higher than exact match but still requires all characters
		containsLen := float64(len(m.Contains))
		possiblePositions := float64(addressLength - len(m.Contains) + 1)
		probability *= (possiblePositions * math.Pow(1.0/32.0, containsLen))
	}
	
	// Calculate probability for required letters and digits
	if m.Letters > 0 {
		// Probability of getting a letter in one position
		pLetter := float64(len(bech32letters)) / 32.0
		probability *= math.Pow(pLetter, float64(m.Letters))
	}
	
	if m.Digits > 0 {
		// Probability of getting a digit in one position
		pDigit := float64(len(bech32digits)) / 32.0
		probability *= math.Pow(pDigit, float64(m.Digits))
	}
	
	// Return expected number of attempts (1/probability)
	if probability > 0 {
		return 1.0 / probability
	}
	return math.MaxFloat64
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	} else if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	} else {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func monitorHashRate(attempts *uint64, quit chan struct{}, expectedAttempts float64) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	
	var lastCount uint64
	var lastTime time.Time = time.Now()
	startTime := time.Now()

	for {
		select {
		case <-quit:
			return
		case <-ticker.C:
			currentCount := atomic.LoadUint64(attempts)
			currentTime := time.Now()
			
			hashRate := float64(currentCount-lastCount) / currentTime.Sub(lastTime).Seconds()
			
			// Calculate ETA
			if hashRate > 0 {
				remainingAttempts := expectedAttempts - float64(currentCount)
				if remainingAttempts < 0 {
					remainingAttempts = 0
				}
				etaSeconds := remainingAttempts / hashRate
				eta := formatDuration(time.Duration(etaSeconds) * time.Second)
				elapsedTime := formatDuration(time.Since(startTime))
				
				fmt.Printf("\rHash rate: %.2f h/s | Total hashes: %d | Elapsed: %s | ETA: %s", 
					hashRate, currentCount, elapsedTime, eta)
			}
			
			lastCount = currentCount
			lastTime = currentTime
		}
	}
}

func findMatchingWalletConcurrent(m matcher, goroutines int) wallet {
	ch := make(chan wallet)
	quit := make(chan struct{})
	defer close(quit)

	var attempts uint64
	expectedAttempts := m.calculateExpectedAttempts()
	
	// Start the hash rate monitor
	go monitorHashRate(&attempts, quit, expectedAttempts)

	for i := 0; i < goroutines; i++ {
		go findMatchingWallets(ch, quit, m, &attempts)
	}
	result := <-ch
	fmt.Println() // Print newline after the hash rate output
	return result
}

const bech32digits = "023456789"
const bech32letters = "acdefghjklmnpqrstuvwxyzACDEFGHJKLMNPQRSTUVWXYZ"

// This is alphanumeric chars minus chars "1", "b", "i", "o" (case insensitive)
const bech32chars = bech32digits + bech32letters

func bech32Only(s string) bool {
	return countUnionChars(s, bech32chars) == len(s)
}

func countUnionChars(s string, letterSet string) int {
	count := 0
	for _, char := range s {
		if strings.Contains(letterSet, string(char)) {
			count++
		}
	}
	return count
}

func main() {
	var walletsToFind = flag.IntP("count", "n", 1, "Amount of matching wallets to find")
	var cpuCount = flag.Int("cpus", runtime.NumCPU(), "Amount of CPU cores to use")

	var mustContain = flag.StringP("contains", "c", "", "A string that the address must contain")
	var mustStartWith = flag.StringP("startswith", "s", "", "A string that the address must start with")
	var mustEndWith = flag.StringP("endswith", "e", "", "A string that the address must end with")
	var letters = flag.IntP("letters", "l", 0, "Amount of letters (a-z) that the address must contain")
	var digits = flag.IntP("digits", "d", 0, "Amount of digits (0-9) that the address must contain")
	flag.Parse()

	if *walletsToFind < 1 {
		fmt.Println("ERROR: The number of wallets to generate must be 1 or more")
		os.Exit(1)
	}
	if *cpuCount < 1 {
		fmt.Println("ERROR: Must use at least 1 CPU core")
		os.Exit(1)
	}

	m := matcher{
		StartsWith: strings.ToLower(*mustStartWith),
		EndsWith:   strings.ToLower(*mustEndWith),
		Contains:   strings.ToLower(*mustContain),
		Letters:    *letters,
		Digits:     *digits,
	}
	matcherValidationErrs := m.ValidationErrors()
	if len(matcherValidationErrs) > 0 {
		for i := 0; i < len(matcherValidationErrs); i++ {
			fmt.Println(matcherValidationErrs[i])
		}
		os.Exit(1)
	}

	var matchingWallet wallet
	for i := 0; i < *walletsToFind; i++ {
		matchingWallet = findMatchingWalletConcurrent(m, *cpuCount)
		fmt.Printf(":::: Matching wallet %d/%d found ::::\n", i+1, *walletsToFind)
		fmt.Println(matchingWallet)
	}
}
