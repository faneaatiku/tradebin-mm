package service

import (
	"context"
	authv1beta1 "cosmossdk.io/api/cosmos/auth/v1beta1"
	"fmt"
	bzeApp "github.com/bze-alphateam/bze/app"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkTx "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	xauthsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/sirupsen/logrus"
	"tradebin-mm/app/internal"
)

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
}

type Broadcaster struct {
	l  logrus.FieldLogger
	tx txConfig

	pk wallet
	cp clientProvider
}

func NewBroadcaster(l logrus.FieldLogger, tx txConfig, pk wallet, cp clientProvider) (*Broadcaster, error) {
	if l == nil || tx == nil || pk == nil || cp == nil {
		return nil, internal.NewInvalidDependenciesErr("NewBroadcaster")
	}

	return &Broadcaster{
		l:  l,
		tx: tx,
		pk: pk,
		cp: cp,
	}, nil
}

func (o *Broadcaster) BroadcastBlock(msgs []sdk.Msg) error {
	return o.Broadcast(msgs, sdkTx.BroadcastMode_BROADCAST_MODE_BLOCK)
}

func (o *Broadcaster) Broadcast(msgs []sdk.Msg, mode sdkTx.BroadcastMode) error {
	if len(msgs) == 0 {
		return fmt.Errorf("no messages to broadcast")
	}

	o.l.Debugf("broadcasting messages: %v", msgs)

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

	encCfg := bzeApp.MakeEncodingConfig()
	txBuilder := encCfg.TxConfig.NewTxBuilder()

	err = txBuilder.SetMsgs(msgs...)
	if err != nil {
		return err
	}
	txBuilder.SetMemo("")

	sigV2 := signing.SignatureV2{
		PubKey: o.pk.GetPrivateKey().PubKey(),
		Data: &signing.SingleSignatureData{
			SignMode:  encCfg.TxConfig.SignModeHandler().DefaultMode(),
			Signature: nil,
		},
		Sequence: baseAcc.Sequence,
	}

	err = txBuilder.SetSignatures(sigV2)
	if err != nil {
		return err
	}

	signerData := xauthsigning.SignerData{
		ChainID:       o.tx.GetChainId(),
		AccountNumber: baseAcc.AccountNumber,
		Sequence:      baseAcc.Sequence,
	}

	sigV2, err = tx.SignWithPrivKey(
		encCfg.TxConfig.SignModeHandler().DefaultMode(),
		signerData,
		txBuilder,
		o.pk.GetPrivateKey(),
		encCfg.TxConfig,
		baseAcc.Sequence,
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
		encCfg.TxConfig.SignModeHandler().DefaultMode(),
		signerData,
		txBuilder,
		o.pk.GetPrivateKey(),
		encCfg.TxConfig,
		baseAcc.Sequence,
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
		return fmt.Errorf("failed to broadcast tx: %v", grpcRes)
	} else {
		o.l.Infof("broadcasted tx: %v", grpcRes.TxResponse.TxHash)
	}

	return nil
}
