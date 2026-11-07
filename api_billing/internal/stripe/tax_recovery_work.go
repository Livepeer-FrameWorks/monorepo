package stripe

import (
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const taxRecoveryWorkPrefix = "fwtx_"
const legacyTaxRecoveryPending = "frameworks_tax_recovery_pending"

func taxRecoveryWorkKeys(metadata map[string]string) []string {
	var keys []string
	for key, value := range metadata {
		if isTaxRecoveryWorkKey(key) && value == "pending" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func isTaxRecoveryWorkKey(key string) bool {
	if !strings.HasPrefix(key, taxRecoveryWorkPrefix) || len(key) != len(taxRecoveryWorkPrefix)+32 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(key, taxRecoveryWorkPrefix))
	return err == nil
}

func newTaxRecoveryWork(metadata, changes map[string]string) (string, error) {
	key := taxRecoveryWorkPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	changes[key] = "pending"
	merged := mergeTaxMetadata(metadata, changes)
	if len(merged) > 50 {
		delete(changes, key)
		return "", errors.New("stripe metadata capacity prevents retaining tax recovery work; collection hold preserved")
	}
	return key, nil
}

func mergeTaxMetadata(metadata, changes map[string]string) map[string]string {
	merged := make(map[string]string, len(metadata)+len(changes))
	for key, value := range metadata {
		if value != "" {
			merged[key] = value
		}
	}
	for key, value := range changes {
		if value == "" {
			delete(merged, key)
		} else {
			merged[key] = value
		}
	}
	return merged
}
