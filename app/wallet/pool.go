package wallet

import (
	"fmt"
	"math/rand"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/go-bip39"
)

const (
	maxPoolSize   = 20
	bip44PathBase = "m/44'/118'/0'/0/%d"
)

type Pool struct {
	addresses    []string
	keyByAddress map[string]*secp256k1.PrivKey
}

func NewPool(mnemonic, addressPrefix string, count int) (*Pool, error) {
	if count < 1 || count > maxPoolSize {
		return nil, fmt.Errorf("count must be between 1 and %d", maxPoolSize)
	}

	config := types.GetConfig()
	config.SetBech32PrefixForAccount(addressPrefix, addressPrefix+"pub")

	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, fmt.Errorf("invalid mnemonic")
	}

	seed := bip39.NewSeed(mnemonic, "")
	master, ch := hd.ComputeMastersFromSeed(seed)

	p := &Pool{
		addresses:    make([]string, count),
		keyByAddress: make(map[string]*secp256k1.PrivKey, count),
	}

	for i := 0; i < count; i++ {
		path := fmt.Sprintf(bip44PathBase, i)
		priv, err := hd.DerivePrivateKeyForPath(master, ch, path)
		if err != nil {
			return nil, fmt.Errorf("failed to derive private key for index %d: %v", i, err)
		}

		privKey := &secp256k1.PrivKey{Key: priv}
		address := types.AccAddress(privKey.PubKey().Address())
		if address.Empty() {
			return nil, fmt.Errorf("failed to generate address for index %d", i)
		}

		addrStr := address.String()
		p.addresses[i] = addrStr
		p.keyByAddress[addrStr] = privKey
	}

	return p, nil
}

func (p *Pool) GetAddressByIndex(index int) (string, error) {
	if index < 0 || index >= len(p.addresses) {
		return "", fmt.Errorf("index %d out of range [0, %d)", index, len(p.addresses))
	}

	return p.addresses[index], nil
}

func (p *Pool) GetPrivKeyByAddress(address string) (*secp256k1.PrivKey, error) {
	key, ok := p.keyByAddress[address]
	if !ok {
		return nil, fmt.Errorf("address %s not found in pool", address)
	}

	return key, nil
}

func (p *Pool) GetFirstAddress() string {
	return p.addresses[0]
}

func (p *Pool) GetRandomAddress() string {
	return p.addresses[rand.Intn(len(p.addresses))]
}

func (p *Pool) Size() int {
	return len(p.addresses)
}
