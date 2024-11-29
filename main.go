package main

import (
	"fmt"
	"github.com/sirupsen/logrus"
	"log"
	"os"
	"time"
	"tradebin-mm/app"
	"tradebin-mm/app/cache"
	"tradebin-mm/app/client"
	"tradebin-mm/app/data_provider"
	"tradebin-mm/app/lock"
	"tradebin-mm/app/service"
	"tradebin-mm/app/wallet"
	"tradebin-mm/config"
)

const (
	defaultAction = "mm"
	actionCancel  = "cancel"
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

	err = cfg.Validate()
	if err != nil {
		log.Fatalf("config validation failed: %v", err)
	}

	l, err := NewLogger(cfg.Logging)
	if err != nil {
		log.Fatalf("could not create logger: %v", err)
	}

	action := defaultAction
	if len(os.Args) > 2 {
		action = os.Args[2]
	}

	switch action {
	case defaultAction:
		startMarketMaking(cfg, l)
	case actionCancel:
		cancelOrders(cfg, l)
	}
}

func cancelOrders(cfg *config.Config, l logrus.FieldLogger) {
	w, err := wallet.NewWallet(cfg.Wallet.Mnemonic)
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
	w, err := wallet.NewWallet(cfg.Wallet.Mnemonic)
	if err != nil {
		log.Fatalf("could not create wallet: %v", err)
	}

	fmt.Printf("Address: %s\n", w.Address)

	grpc, err := getGrpcClient(cfg.Client)
	if err != nil {
		log.Fatalf("could not create grpc client: %v", err)
	}

	volume, err := app.NewVolumeMaker(l)
	if err != nil {
		log.Fatalf("could not create volume maker: %v", err)
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

	orders, err := app.NewOrdersFiller(
		l,
		&cfg.Orders,
		&cfg.Market,
		balances,
		w,
		orderData,
		oService,
	)

	//var done = make(chan bool)
	a, err := NewApp(l, volume, orders)
	if err != nil {
		log.Fatalf("could not create app: %v", err)
	}

	//go a.Start()
	//<-done
	a.Start()

	fmt.Print("program finished. closing in 5 seconds")
	time.Sleep(5 * time.Second)

	fmt.Print("program closed")
}

func getGrpcClient(cl config.Client) (*client.GrpcClient, error) {
	locker := lock.GetInMemoryLocker()

	return client.NewGrpcClient(cl.Grpc, locker)
}
