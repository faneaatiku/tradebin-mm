package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"tradebin-mm/app"
	"tradebin-mm/app/cache"
	"tradebin-mm/app/client"
	"tradebin-mm/app/data_provider"
	"tradebin-mm/app/lock"
	"tradebin-mm/app/service"
	"tradebin-mm/app/wallet"
	"tradebin-mm/config"

	"github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

const (
	defaultAction = "mm"
	actionCancel  = "cancel"
	withdrawAll   = "withdraw-all"
	lpVolume      = "lp-volume"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("not enough arguments provided")
	}

	cfgFile := os.Args[1]
	if cfgFile == "" {
		log.Fatal("please provide config file argument")
	}

	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		log.Fatalf("could not load config: %v", err)
	}

	action := defaultAction
	if len(os.Args) > 2 {
		action = os.Args[2]
	}

	// lp-volume only needs wallet, client, transaction, and liquidity_pool config
	if action != lpVolume {
		err = cfg.Validate()
		if err != nil {
			log.Fatalf("config validation failed: %v", err)
		}
	}

	l, err := NewLogger(cfg.Logging)
	if err != nil {
		log.Fatalf("could not create logger: %v", err)
	}

	switch action {
	case defaultAction:
		startMarketMaking(cfg, l)
	case actionCancel:
		cancelOrders(cfg, l)
	case withdrawAll:
		withdrawAllBalances(cfg, l)
	case lpVolume:
		startLPVolumeMaking(cfg, l)
	}
}

func withdrawAllBalances(cfg *config.Config, l logrus.FieldLogger) {
	w, err := wallet.NewWallet(cfg.Wallet.Mnemonic, cfg.Transaction.GetAddressPrefix())
	if err != nil {
		log.Fatalf("could not create wallet: %v", err)
	}

	address := os.Args[3]
	if address == "" {
		l.Fatal("please provide address argument")
	}

	convAddr, err := types.AccAddressFromBech32(address)
	if err != nil {
		log.Fatalf("could not convert address: %v", err)
	}

	grpc, err := getGrpcClient(cfg.Client)
	if err != nil {
		log.Fatalf("could not create grpc client: %v", err)
	}

	balances, err := data_provider.NewBalanceDataProvider(l, grpc)
	if err != nil {
		log.Fatalf("could not create balance data provider: %v", err)
	}

	broadcaster, err := service.NewBroadcaster(l, &cfg.Transaction, w, grpc)
	if err != nil {
		log.Fatalf("could not create broadcaster: %v", err)
	}

	sender, err := service.NewSender(broadcaster, l)
	if err != nil {
		log.Fatalf("could not create sender: %v", err)
	}

	a, err := app.NewWithdrawAction(l, balances, w, sender)
	if err != nil {
		log.Fatalf("could not create withdraw action: %v", err)
	}

	err = a.WithdrawAll(convAddr)
	if err != nil {
		log.Fatalf("could not withdraw all: %v", err)
	}

	fmt.Printf("all balances withdrawn")
}

func cancelOrders(cfg *config.Config, l logrus.FieldLogger) {
	w, err := wallet.NewWallet(cfg.Wallet.Mnemonic, cfg.Transaction.GetAddressPrefix())
	if err != nil {
		log.Fatalf("could not create wallet: %v", err)
	}

	fmt.Printf("Address: %s\n", w.Address)

	grpc, err := getGrpcClient(cfg.Client)
	if err != nil {
		log.Fatalf("could not create grpc client: %v", err)
	}

	orderData, err := data_provider.NewOrderDataProvider(l, grpc, lock.GetInMemoryLocker(), cache.GetMemoryCache())
	if err != nil {
		log.Fatalf("could not create order data provider: %v", err)
	}

	broadcaster, err := service.NewBroadcaster(l, &cfg.Transaction, w, grpc)
	if err != nil {
		log.Fatalf("could not create broadcaster: %v", err)
	}

	oService, err := service.NewOrderService(l, broadcaster)
	if err != nil {
		log.Fatalf("could not create order service: %v", err)
	}

	cancel, err := app.NewCancelAction(l, oService, orderData, w)
	if err != nil {
		log.Fatalf("could not create cancel action: %v", err)
	}

	err = cancel.CancelAllOrders(&cfg.Market)
	if err != nil {
		log.Fatalf("could not cancel all orders: %v", err)
	}
}

func startMarketMaking(cfg *config.Config, l logrus.FieldLogger) {
	w, err := wallet.NewWallet(cfg.Wallet.Mnemonic, cfg.Transaction.GetAddressPrefix())
	if err != nil {
		log.Fatalf("could not create wallet: %v", err)
	}

	fmt.Printf("Address: %s\n", w.Address)
	grpc, err := getGrpcClient(cfg.Client)
	if err != nil {
		log.Fatalf("could not create grpc client: %v", err)
	}

	balances, err := data_provider.NewBalanceDataProvider(l, grpc)
	if err != nil {
		log.Fatalf("could not create balance data provider: %v", err)
	}

	orderData, err := data_provider.NewOrderDataProvider(l, grpc, lock.GetInMemoryLocker(), cache.GetMemoryCache())
	if err != nil {
		log.Fatalf("could not create order data provider: %v", err)
	}

	broadcaster, err := service.NewBroadcaster(l, &cfg.Transaction, w, grpc)
	if err != nil {
		log.Fatalf("could not create broadcaster: %v", err)
	}

	oService, err := service.NewOrderService(l, broadcaster)
	if err != nil {
		log.Fatalf("could not create order service: %v", err)
	}

	volume, err := app.NewVolumeMaker(
		&cfg.Volume,
		l,
		&cfg.Market,
		balances,
		w,
		orderData,
		oService,
		lock.GetInMemoryLocker(),
		&cfg.Orders,
	)
	if err != nil {
		log.Fatalf("could not create volume maker: %v", err)
	}

	orders, err := app.NewOrdersFiller(
		l,
		&cfg.Orders,
		&cfg.Market,
		balances,
		w,
		orderData,
		oService,
	)

	if err != nil {
		log.Fatalf("could not create orders maker: %v", err)
	}

	a, err := NewApp(l, volume, orders)
	if err != nil {
		log.Fatalf("could not create app: %v", err)
	}

	var done = make(chan bool)
	addSigtermHandler(done)

	go a.Start()
	<-done

	fmt.Print("program closed")
	os.Exit(0)
}

func startLPVolumeMaking(cfg *config.Config, l logrus.FieldLogger) {
	if err := cfg.Wallet.Validate(); err != nil {
		log.Fatalf("wallet config validation failed: %v", err)
	}
	if err := cfg.Client.Validate(); err != nil {
		log.Fatalf("client config validation failed: %v", err)
	}
	if err := cfg.Transaction.Validate(); err != nil {
		log.Fatalf("transaction config validation failed: %v", err)
	}
	if err := cfg.LiquidityPool.Validate(); err != nil {
		log.Fatalf("liquidity pool config validation failed: %v", err)
	}

	w, err := wallet.NewWallet(cfg.Wallet.Mnemonic, cfg.Transaction.GetAddressPrefix())
	if err != nil {
		log.Fatalf("could not create wallet: %v", err)
	}

	fmt.Printf("Address: %s\n", w.Address)

	grpc, err := getGrpcClient(cfg.Client)
	if err != nil {
		log.Fatalf("could not create grpc client: %v", err)
	}

	balances, err := data_provider.NewBalanceDataProvider(l, grpc)
	if err != nil {
		log.Fatalf("could not create balance data provider: %v", err)
	}

	broadcaster, err := service.NewBroadcaster(l, &cfg.Transaction, w, grpc)
	if err != nil {
		log.Fatalf("could not create broadcaster: %v", err)
	}

	lpDataProvider, err := data_provider.NewLiquidityPoolProvider(l, grpc)
	if err != nil {
		log.Fatalf("could not create liquidity pool data provider: %v", err)
	}

	lpVolumeMaker, err := app.NewLPVolumeMaker(l, lpDataProvider, balances, w, broadcaster, cfg.LiquidityPool.Id, cfg.LiquidityPool.Slippage)
	if err != nil {
		log.Fatalf("could not create lp volume maker: %v", err)
	}

	var done = make(chan bool)
	addSigtermHandler(done)

	go func() {
		for {
			err := lpVolumeMaker.MakeVolume()
			if err != nil {
				l.WithError(err).Error("lp volume maker error")
			}
			time.Sleep(time.Duration(cfg.LiquidityPool.Interval) * time.Second)
		}
	}()

	<-done
	fmt.Print("program closed")
	os.Exit(0)
}

func getGrpcClient(cl config.Client) (*client.GrpcClient, error) {
	locker := lock.GetInMemoryLocker()

	return client.NewGrpcClient(cl.Grpc, cl.TLSEnabled, locker)
}

func addSigtermHandler(done chan<- bool) {
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		done <- true
	}()
}
