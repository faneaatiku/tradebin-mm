package wallet

import (
	"fmt"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/go-bip39"
)

const (
	bip44Path = "m/44'/118'/0'/0/0"
	prefix    = "bze"
)

type Wallet struct {
	Address types.AccAddress
	Key     *secp256k1.PrivKey
}

func NewWallet(mnemonic string) (*Wallet, error) {
	config := types.GetConfig()
	config.SetBech32PrefixForAccount(prefix, prefix+"pub")

	// Validate the mnemonic
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, fmt.Errorf("invalid mnemonic")
	}

	// Generate the seed from the mnemonic
	seed := bip39.NewSeed(mnemonic, "")
	master, ch := hd.ComputeMastersFromSeed(seed)
	priv, err := hd.DerivePrivateKeyForPath(master, ch, bip44Path)
	if err != nil {
		return nil, fmt.Errorf("failed to derive private key: %v", err)
	}

	// Wrap the private key bytes into a secp256k1 private key object
	privKey := &secp256k1.PrivKey{Key: priv}

	// Generate the corresponding address
	address := types.AccAddress(privKey.PubKey().Address())
	if address.Empty() {
		return nil, fmt.Errorf("failed to generate address")
	}

	return &Wallet{
		Address: address,
		Key:     privKey,
	}, nil
}

func (w *Wallet) GetAddress() types.AccAddress {
	return w.Address
}

func (w *Wallet) GetPrivateKey() *secp256k1.PrivKey {
	return w.Key
}
