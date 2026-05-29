package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	projects, err := LoadProjects("projects.yaml")
	if err != nil {
		log.Fatalf("projects.yaml: %v", err)
	}
	// JIRA_QORK_PROJECTS env var overrides projects.yaml when set.
	if len(cfg.JiraQORKProjects) > 0 {
		projects.QORKProjects = cfg.JiraQORKProjects
	}
	signals, err := LoadSignals("signals.yaml")
	if err != nil {
		log.Fatalf("signals.yaml: %v", err)
	}
	store, err := OpenStore(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer store.Close()

	api := slack.New(cfg.SlackBotToken,
		slack.OptionAppLevelToken(cfg.SlackAppToken))
	auth, err := api.AuthTest()
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	botID := auth.UserID
	log.Printf("authenticated as %s (%s)", auth.User, botID)

	jira := NewJiraClient(cfg.JiraBaseURL, cfg.JiraEmail, cfg.JiraAPIToken)
	claudeRunner := NewClaudeRunner()

	appMetrics := NewAppMetrics()

	executor := &JobExecutor{
		Store: store, Slack: api, Claude: claudeRunner,
		Jira:      newMetricsJira(jira, appMetrics),
		Projects:  projects, Signals: signals, BotUserID: botID,
	}
	worker := NewWorker(cfg.Workers, cfg.QueueSize, WorkerDeps{
		Execute: func(ctx context.Context, j job) error {
			start := time.Now()
			err := executor.Execute(ctx, j)
			appMetrics.RecordJob(j.kind(), time.Since(start))
			return err
		},
		Logger: log.Printf,
	})
	worker.Start()

	watched := map[string]bool{}
	for _, c := range cfg.WatchedChannels {
		watched[c] = true
	}
	router := &Router{
		Store: store, Slack: api, BotUserID: botID,
		WatchedChannels: watched, ApprovalThreshold: cfg.ApprovalThreshold,
		Signals: signals, Projects: projects, Worker: worker, Config: cfg,
		Claude:           claudeRunner,
		ReminderInterval: time.Duration(cfg.ReminderIntervalDays) * 24 * time.Hour,
	}

	tk := &Ticker{
		Store: store, Slack: api, Worker: worker,
		ReminderEvery: time.Duration(cfg.ReminderIntervalDays) * 24 * time.Hour,
		WarnAt:        time.Duration(cfg.WarningAtDays) * 24 * time.Hour,
		ArchiveAt:     time.Duration(cfg.WarningAtDays+cfg.ArchiveGraceDays) * 24 * time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go runTicker(ctx, tk)

	health := NewHealthServer(HealthDeps{Store: store, Worker: worker, TriggerToken: cfg.TriggerToken, Slack: api, Metrics: appMetrics})
	go func() {
		addr := fmt.Sprintf(":%d", cfg.HealthPort)
		log.Printf("health listening on %s", addr)
		if err := health.ListenAndServe(addr); err != nil {
			log.Printf("health: %v", err)
		}
	}()

	// Socket Mode event loop.
	sm := socketmode.New(api)
	go runSocketMode(ctx, sm, router)
	go func() {
		if err := sm.RunContext(ctx); err != nil && ctx.Err() == nil {
			log.Printf("socketmode: %v", err)
		}
	}()

	// Wait for signal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Print("shutdown signal received")
	cancel()
	worker.Stop(60 * time.Second)
	log.Print("shutdown complete")
}

func runTicker(ctx context.Context, tk *Ticker) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	tk.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tk.Tick(ctx)
		}
	}
}

func runSocketMode(ctx context.Context, sm *socketmode.Client, r *Router) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sm.Events:
			if !ok {
				return
			}
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				payload, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					continue
				}
				sm.Ack(*evt.Request)
				handleEventsAPI(r, payload)
			}
		}
	}
}

func handleEventsAPI(r *Router, e slackevents.EventsAPIEvent) {
	switch e.Type {
	case slackevents.CallbackEvent:
		switch ev := e.InnerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			switch ev.SubType {
			case "message_deleted":
				r.HandleMessageDeleted(MessageEvent{
					Channel: ev.Channel,
					TS:      ev.DeletedTimeStamp,
				})
			case "message_changed":
				if ev.Message != nil {
					r.HandleMessageChanged(MessageEvent{
						Channel: ev.Channel,
						TS:      ev.Message.Timestamp,
						Text:    ev.Message.Text,
						User:    ev.Message.User,
					})
				}
			default:
				r.HandleMessage(MessageEvent{
					Channel:  ev.Channel,
					TS:       ev.TimeStamp,
					ThreadTS: ev.ThreadTimeStamp,
					User:     ev.User,
					Text:     ev.Text,
				})
			}
		case *slackevents.AppMentionEvent:
			r.HandleAppMention(MessageEvent{
				Channel:  ev.Channel,
				TS:       ev.TimeStamp,
				ThreadTS: ev.ThreadTimeStamp,
				User:     ev.User,
				Text:     ev.Text,
			})
		case *slackevents.ReactionAddedEvent:
			r.HandleReactionAdded(ReactionEvent{
				User:    ev.User,
				Channel: ev.Item.Channel,
				TS:      ev.Item.Timestamp,
				Name:    ev.Reaction,
			})
		case *slackevents.ReactionRemovedEvent:
			r.HandleReactionRemoved(ReactionEvent{
				User:    ev.User,
				Channel: ev.Item.Channel,
				TS:      ev.Item.Timestamp,
				Name:    ev.Reaction,
			})
		}
	}
}
