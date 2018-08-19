package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/eximchain/eth-client/quorum"
	"github.com/eximchain/go-ethereum/accounts"
	"github.com/eximchain/go-ethereum/accounts/keystore"

	vault "github.com/hashicorp/vault/api"
	awsauth "github.com/hashicorp/vault/builtin/credential/aws"
)

// Expects to be running in EC2
func GetRole() (string, error) {
	svc := ec2metadata.New(session.New())
	iam, err := svc.IAMInfo()
	if err != nil {
		return "", err
	}
	// Our instance profile conveniently has the same name as the role
	profile := iam.InstanceProfileArn
	splitArn := strings.Split(profile, "/")
	if len(splitArn) < 2 {
		return "", fmt.Errorf("no / character found in instance profile ARN")
	}
	role := splitArn[1]
	return role, nil
}

func LoginAws(v *vault.Client) (string, error) {
	loginData, err := awsauth.GenerateLoginData("", "", "", "")
	if err != nil {
		return "", err
	}
	if loginData == nil {
		return "", fmt.Errorf("got nil response from GenerateLoginData")
	}

	role, err := GetRole()
	if err != nil {
		return "", err
	}
	loginData["role"] = role

	path := "auth/aws/login"

	secret, err := v.Logical().Write(path, loginData)
	if err != nil {
		return "", err
	}
	if secret == nil {
		return "", fmt.Errorf("empty response from credential provider")
	}
	if secret.Auth == nil {
		return "", fmt.Errorf("auth secret has no auth data")
	}

	token := secret.Auth.ClientToken
	return token, nil
}

func accessControl(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			return
		}

		h.ServeHTTP(w, r)
	})
}

func RunServerCommand(args []string) {
	serverCommand := flag.NewFlagSet("server", flag.ExitOnError)
	vaultAddressFlag := serverCommand.String("vault-address", "http://127.0.0.1:8200", "The address at which vault can be accessed")
	quorumAddressFlag := serverCommand.String("quorum-address", "http://127.0.0.1:8545", "The address at which the quorum node can be reached")
	authTokenFlag := serverCommand.String("auth-token", "", "An auth token to use instead of AWS authorization, for help with testing")
	keyDirFlag := serverCommand.String("keystore", "/home/ubuntu/.ethereum/keystore", "The directory to use as a keystore")
	serverCommand.Parse(args)

	// Vault client setup
	vaultCFG := vault.DefaultConfig()
	// TODO: Put this behind a command line flag so we can test
	vaultCFG.Address = *vaultAddressFlag

	var err error
	vaultClient, err := vault.NewClient(vaultCFG)
	if err != nil {
		log.Fatal(err)
	}

	var token string
	if *authTokenFlag != "" {
		token = *authTokenFlag
	} else {
		token, err = LoginAws(vaultClient)
		if err != nil {
			log.Fatal(err)
		}
	}
	vaultClient.SetToken(token)

	// Quorum client setup
	quorumAddress := *quorumAddressFlag
	quorumClient, err := quorum.Dial(quorumAddress)
	if err != nil {
		log.Fatal(err)
	}

	// Keystore setup
	gethKeyDir := *keyDirFlag
	gethKeystore := keystore.NewKeyStore(gethKeyDir, keystore.StandardScryptN, keystore.StandardScryptP)

	svc := transactionExecutorService{
		vaultClient:   vaultClient,
		keystore:      gethKeystore,
		quorumClient:  quorumClient,
		quorumAddress: quorumAddress,
		accountCache:  make(map[string]accounts.Account),
	}

	db := &BoltDB{}
	err = db.Open("eximchain.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Listen on unix socket for user management commands
	listener := listenIPC(db)

	mux := http.NewServeMux()
	mux.Handle("/rpc", Auth(db, MakeRPCHandler(svc)))

	http.Handle("/", accessControl(mux))

	stopChan := make(chan os.Signal, 1)

	// subscribe to SIGINT signals
	signal.Notify(stopChan, os.Interrupt)

	srv := &http.Server{Addr: ":8080"}

	go func() {
		// service connections
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("listen: %s\n", err)
			stopChan <- os.Interrupt
		}
	}()

	log.Println("Listening on", srv.Addr)

	// wait for SIGINT
	<-stopChan

	log.Println("Shutting down server...")

	if listener != nil {
		if err := listener.Close(); err != nil {
			log.Printf("IPC Close: %s", err)
		}
	}

	// shut down gracefully, but wait no longer than 5 seconds before halting
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv.Shutdown(ctx)

	log.Println("Server gracefully stopped")
}
