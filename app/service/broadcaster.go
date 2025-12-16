package service

import (
	"context"
	"fmt"
	"sync"
	"tradebin-mm/app/internal"

	authv1beta1 "cosmossdk.io/api/cosmos/auth/v1beta1"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/std"
	"github.com/cosmos/cosmos-sdk/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkTx "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	xauthsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/sirupsen/logrus"
)

// EncodingConfig specifies the concrete encoding types to use for a given app.
type EncodingConfig struct {
	InterfaceRegistry codectypes.InterfaceRegistry
	Codec             codec.Codec
	TxConfig          client.TxConfig
}

// makeEncodingConfig creates an EncodingConfig for the broadcaster.
func makeEncodingConfig() EncodingConfig {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	std.RegisterInterfaces(interfaceRegistry)
	protoCodec := codec.NewProtoCodec(interfaceRegistry)
	txConfig := authtx.NewTxConfig(protoCodec, authtx.DefaultSignModes)

	return EncodingConfig{
		InterfaceRegistry: interfaceRegistry,
		Codec:             protoCodec,
		TxConfig:          txConfig,
	}
}

type clientProvider interface {
	GetServiceClient() (sdkTx.ServiceClient, error)
	GetAuthQueryClient() (authv1beta1.QueryClient, error)
}

type wallet interface {
	GetPrivateKey() *secp256k1.PrivKey
	GetAddress() types.AccAddress
}

type txConfig interface {
	GetGasPrices() string
	GetGasAdjustment() float64
	GetChainId() string
	GetAddressPrefix() string
}

type Broadcaster struct {
	l  logrus.FieldLogger
	tx txConfig

	pk wallet
	cp clientProvider

	// Sequence tracking
	mu             sync.Mutex
	cachedSequence uint64
	sequenceValid  bool
	accountNumber  uint64
}

func NewBroadcaster(l logrus.FieldLogger, tx txConfig, pk wallet, cp clientProvider) (*Broadcaster, error) {
	if l == nil || tx == nil || pk == nil || cp == nil {
		return nil, internal.NewInvalidDependenciesErr("NewBroadcaster")
	}

	AccountAddressPrefix := tx.GetAddressPrefix()
	// Set prefixes
	accountPubKeyPrefix := AccountAddressPrefix + "pub"
	validatorAddressPrefix := AccountAddressPrefix + "valoper"
	validatorPubKeyPrefix := AccountAddressPrefix + "valoperpub"
	consNodeAddressPrefix := AccountAddressPrefix + "valcons"
	consNodePubKeyPrefix := AccountAddressPrefix + "valconspub"

	// Set and seal config
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount(AccountAddressPrefix, accountPubKeyPrefix)
	config.SetBech32PrefixForValidator(validatorAddressPrefix, validatorPubKeyPrefix)
	config.SetBech32PrefixForConsensusNode(consNodeAddressPrefix, consNodePubKeyPrefix)

	return &Broadcaster{
		l:  l,
		tx: tx,
		pk: pk,
		cp: cp,
	}, nil
}

func (o *Broadcaster) BroadcastBlock(msgs []sdk.Msg) error {
	return o.Broadcast(msgs, sdkTx.BroadcastMode_BROADCAST_MODE_SYNC)
}

func (o *Broadcaster) Broadcast(msgs []sdk.Msg, mode sdkTx.BroadcastMode) error {
	// Lock to prevent concurrent broadcasts with sequence mismatch
	o.mu.Lock()
	defer o.mu.Unlock()

	if len(msgs) == 0 {
		return fmt.Errorf("no messages to broadcast")
	}

	o.l.Debugf("broadcasting messages: %v", msgs)

	var sequence uint64
	var accountNumber uint64

	// Use cached sequence if valid, otherwise query from chain
	if o.sequenceValid {
		sequence = o.cachedSequence
		accountNumber = o.accountNumber
		o.l.Debugf("using cached sequence: %d", sequence)
	} else {
		authCl, err := o.cp.GetAuthQueryClient()
		if err != nil {
			return err
		}

		resp, err := authCl.Account(context.Background(), &authv1beta1.QueryAccountRequest{Address: o.pk.GetAddress().String()})
		if err != nil {
			return err
		}

		acc := resp.GetAccount()
		if acc == nil {
			return fmt.Errorf("account not found")
		}

		baseAcc := &authv1beta1.BaseAccount{}
		if err := acc.UnmarshalTo(baseAcc); err != nil {
			return fmt.Errorf("failed to unmarshal account: %w", err)
		}

		sequence = baseAcc.Sequence
		accountNumber = baseAcc.AccountNumber
		o.l.Debugf("queried sequence from chain: %d", sequence)
	}

	encCfg := makeEncodingConfig()
	txBuilder := encCfg.TxConfig.NewTxBuilder()

	err := txBuilder.SetMsgs(msgs...)
	if err != nil {
		return err
	}
	txBuilder.SetMemo("")

	sigV2 := signing.SignatureV2{
		PubKey: o.pk.GetPrivateKey().PubKey(),
		Data: &signing.SingleSignatureData{
			SignMode:  signing.SignMode_SIGN_MODE_DIRECT,
			Signature: nil,
		},
		Sequence: sequence,
	}

	err = txBuilder.SetSignatures(sigV2)
	if err != nil {
		return err
	}

	signerData := xauthsigning.SignerData{
		ChainID:       o.tx.GetChainId(),
		AccountNumber: accountNumber,
		Sequence:      sequence,
	}

	sigV2, err = tx.SignWithPrivKey(
		context.Background(),
		signing.SignMode_SIGN_MODE_DIRECT,
		signerData,
		txBuilder,
		o.pk.GetPrivateKey(),
		encCfg.TxConfig,
		sequence,
	)
	if err != nil {
		return err
	}

	err = txBuilder.SetSignatures(sigV2)
	if err != nil {
		return err
	}

	txBytes, err := encCfg.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return err
	}

	cl, err := o.cp.GetServiceClient()
	if err != nil {
		return err
	}

	o.l.Debug("simulating tx")
	simulate, err := cl.Simulate(
		context.Background(),
		&sdkTx.SimulateRequest{
			TxBytes: txBytes,
		},
	)
	if err != nil {
		o.sequenceValid = false
		return err
	}
	o.l.Debugf("simulated tx: %v", simulate.GasInfo)

	fee, err := getFee(simulate.GasInfo.GasUsed, o.tx.GetGasPrices())
	if err != nil {
		return err
	}
	o.l.WithField("fee", fee).Debug("calculated fee")

	gasLimit := float64(simulate.GasInfo.GasUsed) * o.tx.GetGasAdjustment()
	txBuilder.SetGasLimit(uint64(gasLimit))
	txBuilder.SetFeeAmount(fee)

	sigV2, err = tx.SignWithPrivKey(
		context.Background(),
		signing.SignMode_SIGN_MODE_DIRECT,
		signerData,
		txBuilder,
		o.pk.GetPrivateKey(),
		encCfg.TxConfig,
		sequence,
	)
	if err != nil {
		return err
	}

	err = txBuilder.SetSignatures(sigV2)
	if err != nil {
		return err
	}

	txBytes, err = encCfg.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return err
	}

	o.l.Debug("broadcasting tx")
	grpcRes, err := cl.BroadcastTx(
		context.Background(),
		&sdkTx.BroadcastTxRequest{
			Mode:    mode,
			TxBytes: txBytes, // Proto-binary of the signed transaction, see previous step.
		},
	)

	if err != nil {
		return err
	}

	if grpcRes.TxResponse.Code != 0 {
		// Invalidate cached sequence on error
		o.sequenceValid = false
		return fmt.Errorf("failed to broadcast tx: %v", grpcRes)
	} else {
		o.l.Infof("broadcasted tx: %v", grpcRes.TxResponse.TxHash)
		// Update cached sequence for next transaction
		o.cachedSequence = sequence + 1
		o.accountNumber = accountNumber
		o.sequenceValid = true
	}

	return nil
}
