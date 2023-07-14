package main

import (
	"context"
	_ "embed"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/log"
	"github.com/disgoorg/snowflake/v2"
	"github.com/topi314/reddit-discord-bot/redditbot"
	"golang.org/x/oauth2"
)

var (
	Name      = "reddit-discord-bot"
	Namespace = "github.com/topi314/reddit-discord-bot"

	Version = "unknown"
	Commit  = "unknown"
)

var (
	//go:embed schema.sql
	schema string

	//go:embed reddit.png
	redditIcon []byte
)

func main() {
	log.Infof("starting reddit-discord-bot version: %s (%s)", Version, Commit)
	cfg, err := redditbot.ReadConfig()
	if err != nil {
		log.Fatal("error reading config:", err.Error())
	}

	log.SetLevel(cfg.Log.Level)
	log.SetFlags(cfg.Log.Flags())

	log.Info("Config:", cfg)
	if err = cfg.Validate(); err != nil {
		log.Fatalf(err.Error())
	}

	client, err := disgo.New(cfg.Discord.Token,
		bot.WithDefaultGateway(),
	)
	if err != nil {
		log.Fatal("error creating client:", err.Error())
	}

	reddit, err := redditbot.NewReddit(cfg.Reddit)
	if err != nil {
		log.Fatal("error creating reddit client:", err.Error())
	}

	db, err := redditbot.NewDB(cfg.Database, schema)
	if err != nil {
		log.Fatal("error creating database client:", err.Error())
	}

	meter, err := newMeter(cfg.Otel)
	if err != nil {
		log.Fatal("error creating meter:", err.Error())
	}

	b := redditbot.Bot{
		Cfg:        cfg,
		RedditIcon: redditIcon,
		Meter:      meter,
		Client:     client,
		Reddit:     reddit,
		DB:         db,
		Rand:       rand.New(rand.NewSource(time.Now().UnixNano())),
		DiscordConfig: &oauth2.Config{
			ClientID:     client.ApplicationID().String(),
			ClientSecret: cfg.Discord.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:   "https://discord.com/api/oauth2/authorize",
				TokenURL:  "https://discord.com/api/oauth2/token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
			RedirectURL: cfg.Server.RedirectURL,
			Scopes: []string{
				string(discord.OAuth2ScopeWebhookIncoming),
			},
		},
		States:    map[string]redditbot.SetupState{},
		LastPosts: map[snowflake.ID]string{},
	}

	if cfg.Otel.Enabled {
		if err = b.InitMetrics(); err != nil {
			log.Fatal("error initializing metrics:", err.Error())
		}
	}

	if cfg.Server.Enabled {
		mux := http.NewServeMux()
		mux.HandleFunc(cfg.Server.Endpoint, b.OnDiscordCallback)
		b.Server = &http.Server{
			Addr:    cfg.Server.ListenAddr,
			Handler: mux,
		}
	}

	b.Client.AddEventListeners(bot.NewListenerFunc(b.OnApplicationCommand))

	if _, err = client.Rest().SetGlobalCommands(client.ApplicationID(), redditbot.Commands); err != nil {
		log.Fatal("error setting global commands:", err.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = client.OpenGateway(ctx); err != nil {
		log.Fatal("error opening gateway:", err.Error())
	}

	go b.ListenSubreddits()

	if cfg.Server.Enabled {
		go b.ListenAndServe()
		defer b.Server.Shutdown(context.Background())
	}

	defer log.Info("exiting...")

	s := make(chan os.Signal, 1)
	signal.Notify(s, syscall.SIGINT, syscall.SIGTERM)
	<-s
}
