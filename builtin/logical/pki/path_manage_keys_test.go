package pki

import (
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"testing"

	"github.com/hashicorp/vault/sdk/helper/certutil"

	"github.com/hashicorp/vault/sdk/logical"
	"github.com/stretchr/testify/require"
)

func TestPKI_PathManageKeys_GenerateInternalKeys(t *testing.T) {
	b, s := createBackendWithStorage(t)

	tests := []struct {
		name           string
		keyType        string
		keyBits        []int
		wantLogicalErr bool
	}{
		{"all-defaults", "", []int{0}, false},
		{"rsa", "rsa", []int{0, 2048, 3072, 4096}, false},
		{"ec", "ec", []int{0, 224, 256, 384, 521}, false},
		{"ed25519", "ed25519", []int{0}, false},
		{"error-rsa", "rsa", []int{-1, 343444}, true},
		{"error-ec", "ec", []int{-1, 3434324}, true},
		{"error-bad-type", "dskjfkdsfjdkf", []int{0}, true},
	}
	for _, tt := range tests {
		for _, keyBitParam := range tt.keyBits {
			keyName := fmt.Sprintf("%s-%d", tt.name, keyBitParam)
			t.Run(keyName, func(t *testing.T) {
				data := make(map[string]interface{})
				if tt.keyType != "" {
					data["key_type"] = tt.keyType
				}
				if keyBitParam != 0 {
					data["key_bits"] = keyBitParam
				}
				keyName = genUuid() + "-" + tt.keyType + "-key-name"
				data["key_name"] = keyName
				resp, err := b.HandleRequest(context.Background(), &logical.Request{
					Operation:  logical.UpdateOperation,
					Path:       "keys/generate/internal",
					Storage:    s,
					Data:       data,
					MountPoint: "pki/",
				})
				require.NoError(t, err,
					"Failed generating key with values key_type:%s key_bits:%d key_name:%s", tt.keyType, keyBitParam, keyName)
				require.NotNil(t, resp,
					"Got nil response generating key with values key_type:%s key_bits:%d key_name:%s", tt.keyType, keyBitParam, keyName)
				if tt.wantLogicalErr {
					require.True(t, resp.IsError(), "expected logical error but the request passed:\n%#v", resp)
				} else {
					require.False(t, resp.IsError(),
						"Got logical error response when not expecting one, "+
							"generating key with values key_type:%s key_bits:%d key_name:%s\n%s", tt.keyType, keyBitParam, keyName, resp.Error())

					// Special case our all-defaults
					if tt.keyType == "" {
						tt.keyType = "rsa"
					}

					require.Equal(t, tt.keyType, resp.Data["key_type"], "key_type field contained an invalid type")
					require.NotEmpty(t, resp.Data["key_id"], "returned an empty key_id field, should never happen")
					require.Equal(t, keyName, resp.Data["key_name"], "key name was not processed correctly")
					require.Nil(t, resp.Data["private_key"], "private_key field should not appear in internal generation type.")
				}
			})
		}
	}
}

func TestPKI_PathManageKeys_GenerateExportedKeys(t *testing.T) {
	// We tested a lot of the logic above within the internal test, so just make sure we honor the exported contract
	b, s := createBackendWithStorage(t)

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "keys/generate/exported",
		Storage:   s,
		Data: map[string]interface{}{
			"key_type": "ec",
			"key_bits": 224,
		},
		MountPoint: "pki/",
	})
	require.NoError(t, err, "Failed generating exported key")
	require.NotNil(t, resp, "Got nil response generating exported key")
	require.Equal(t, "ec", resp.Data["key_type"], "key_type field contained an invalid type")
	require.NotEmpty(t, resp.Data["key_id"], "returned an empty key_id field, should never happen")
	require.Empty(t, resp.Data["key_name"], "key name should have been empty but was not")
	require.NotEmpty(t, resp.Data["private_key"], "private_key field should not be empty in exported generation type.")

	// Make sure we can decode our private key as expected
	keyData := resp.Data["private_key"].(string)
	block, rest := pem.Decode([]byte(keyData))
	require.Empty(t, rest, "should not have had any trailing data")
	require.NotEmpty(t, block, "failed decoding pem block")

	key, err := x509.ParseECPrivateKey(block.Bytes)
	require.NoError(t, err, "failed parsing pem block as ec private key")
	require.Equal(t, elliptic.P224(), key.Curve, "got unexpected curve value in returned private key")
}

func TestPKI_PathManageKeys_ImportKeyBundle(t *testing.T) {
	b, s := createBackendWithStorage(t)

	bundle1, err := certutil.CreateKeyBundle("ec", 224, rand.Reader)
	require.NoError(t, err, "failed generating an ec key bundle")
	bundle2, err := certutil.CreateKeyBundle("rsa", 2048, rand.Reader)
	require.NoError(t, err, "failed generating an rsa key bundle")
	pem1, err := bundle1.ToPrivateKeyPemString()
	require.NoError(t, err, "failed converting ec key to pem")
	pem2, err := bundle2.ToPrivateKeyPemString()
	require.NoError(t, err, "failed converting rsa key to pem")

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "keys/import",
		Storage:   s,
		Data: map[string]interface{}{
			"key_name":   "my-ec-key",
			"pem_bundle": pem1,
		},
		MountPoint: "pki/",
	})
	require.NoError(t, err, "Failed importing ec key")
	require.NotNil(t, resp, "Got nil response importing ec key")
	require.False(t, resp.IsError(), "received an error response: %v", resp.Error())
	require.NotEmpty(t, resp.Data["key_id"], "key id for ec import response was empty")
	require.Equal(t, "my-ec-key", resp.Data["key_name"], "key_name was incorrect for ec key")
	require.Equal(t, certutil.ECPrivateKey, resp.Data["key_type"])
	keyId1 := resp.Data["key_id"]

	resp, err = b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "keys/import",
		Storage:   s,
		Data: map[string]interface{}{
			"key_name":   "my-rsa-key",
			"pem_bundle": pem2,
		},
		MountPoint: "pki/",
	})
	require.NoError(t, err, "Failed importing rsa key")
	require.NotNil(t, resp, "Got nil response importing rsa key")
	require.False(t, resp.IsError(), "received an error response: %v", resp.Error())
	require.NotEmpty(t, resp.Data["key_id"], "key id for rsa import response was empty")
	require.Equal(t, "my-rsa-key", resp.Data["key_name"], "key_name was incorrect for ec key")
	require.Equal(t, certutil.RSAPrivateKey, resp.Data["key_type"])
	keyId2 := resp.Data["key_id"]

	require.NotEqual(t, keyId1, keyId2)

	// Attempt to reimport the same key with a different name.
	resp, err = b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "keys/import",
		Storage:   s,
		Data: map[string]interface{}{
			"key_name":   "my-new-ec-key",
			"pem_bundle": pem1,
		},
		MountPoint: "pki/",
	})
	require.NoError(t, err, "Failed importing the same ec key")
	require.NotNil(t, resp, "Got nil response importing the same ec key")
	require.False(t, resp.IsError(), "received an error response: %v", resp.Error())
	require.NotEmpty(t, resp.Data["key_id"], "key id for ec import response was empty")
	// Note we should receive back the original name, not the new updated name.
	require.Equal(t, "my-ec-key", resp.Data["key_name"], "key_name was incorrect for ec key")
	require.Equal(t, certutil.ECPrivateKey, resp.Data["key_type"])
	keyIdReimport := resp.Data["key_id"]
	require.Equal(t, keyId1, keyIdReimport, "the re-imported key did not return the same key id")

	// Make sure we can not reuse an existing name across different keys.
	bundle3, err := certutil.CreateKeyBundle("ec", 224, rand.Reader)
	require.NoError(t, err, "failed generating an ec key bundle")
	pem3, err := bundle3.ToPrivateKeyPemString()
	require.NoError(t, err, "failed converting rsa key to pem")
	resp, err = b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "keys/import",
		Storage:   s,
		Data: map[string]interface{}{
			"key_name":   "my-ec-key",
			"pem_bundle": pem3,
		},
		MountPoint: "pki/",
	})
	require.NoError(t, err, "Failed importing the same ec key")
	require.NotNil(t, resp, "Got nil response importing the same ec key")
	require.True(t, resp.IsError(), "should have received an error response importing a key with a re-used name")

	keys, _ := listKeys(context.Background(), s)
	for _, keyId := range keys {
		key, _ := fetchKeyById(context.Background(), s, keyId)
		t.Logf("%s:%s", key.ID, key.Name)
	}
}

func TestPKI_PathManageKeys_ImportKeyBundleBadData(t *testing.T) {
	b, s := createBackendWithStorage(t)

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "keys/import",
		Storage:   s,
		Data: map[string]interface{}{
			"key_name":   "my-ec-key",
			"pem_bundle": "this-is-not-a-pem-bundle",
		},
		MountPoint: "pki/",
	})
	require.NoError(t, err, "got a 500 error type response from a bad pem bundle")
	require.NotNil(t, resp, "Got nil response importing a bad pem bundle")
	require.True(t, resp.IsError(), "should have received an error response importing a bad pem bundle")

	// Make sure we also bomb on a proper certificate
	bundle := genCertBundle(t, b, s)
	resp, err = b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "keys/import",
		Storage:   s,
		Data: map[string]interface{}{
			"pem_bundle": bundle.Certificate,
		},
		MountPoint: "pki/",
	})
	require.NoError(t, err, "got a 500 error type response from a certificate pem bundle")
	require.NotNil(t, resp, "Got nil response importing a certificate bundle")
	require.True(t, resp.IsError(), "should have received an error response importing a certificate pem bundle")
}

func TestPKI_PathManageKeys_ImportKeyRejectsMultipleKeys(t *testing.T) {
	b, s := createBackendWithStorage(t)

	bundle1, err := certutil.CreateKeyBundle("ec", 224, rand.Reader)
	require.NoError(t, err, "failed generating an ec key bundle")
	bundle2, err := certutil.CreateKeyBundle("rsa", 2048, rand.Reader)
	require.NoError(t, err, "failed generating an rsa key bundle")
	pem1, err := bundle1.ToPrivateKeyPemString()
	require.NoError(t, err, "failed converting ec key to pem")
	pem2, err := bundle2.ToPrivateKeyPemString()
	require.NoError(t, err, "failed converting rsa key to pem")

	importPem := pem1 + "\n" + pem2

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "keys/import",
		Storage:   s,
		Data: map[string]interface{}{
			"key_name":   "my-ec-key",
			"pem_bundle": importPem,
		},
		MountPoint: "pki/",
	})
	require.NoError(t, err, "got a 500 error type response from a bad pem bundle")
	require.NotNil(t, resp, "Got nil response importing a bad pem bundle")
	require.True(t, resp.IsError(), "should have received an error response importing a pem bundle with more than 1 key")

	ctx := context.Background()
	keys, _ := listKeys(ctx, s)
	for _, keyId := range keys {
		id, _ := fetchKeyById(ctx, s, keyId)
		t.Logf("%s:%s", id.ID, id.Name)
	}
}
