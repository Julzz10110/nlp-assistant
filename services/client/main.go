package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"google.golang.org/protobuf/types/known/timestamppb"

	assistantpb "github.com/Julzz10110/nlp-assistant/api/proto/assistantpb"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	addr := os.Getenv("ORCHESTRATOR_ADDR")
	if addr == "" {
		addr = "localhost:50051"
	}

	conn, err := grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("failed to dial orchestrator: %v", err)
	}
	defer conn.Close()

	client := assistantpb.NewConversationOrchestratorClient(conn)

	stream, err := client.Converse(ctx)
	if err != nil {
		log.Fatalf("failed to open Converse stream: %v", err)
	}

	sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	userID := "user-cli"

	fmt.Printf("Connected to assistant (session_id=%s). Type messages, Ctrl+C to exit.\n", sessionID)

	// Goroutine for reading server responses.
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				log.Printf("stream recv error: %v", err)
				return
			}

			switch p := resp.Payload.(type) {
			case *assistantpb.ServerMessage_Text:
				fmt.Printf("Assistant: %s\n", p.Text.GetText())
			case *assistantpb.ServerMessage_Confirmation:
				fmt.Printf("Assistant (clarification): %s\n", p.Confirmation.GetQuestion())
			case *assistantpb.ServerMessage_Error:
				fmt.Printf("Assistant (error %s): %s\n", p.Error.GetCode(), p.Error.GetMessage())
			default:
				fmt.Printf("Assistant (other message): %#v\n", resp)
			}
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("You: ")
		if !scanner.Scan() {
			break
		}
		text := scanner.Text()
		if text == "" {
			continue
		}

		req := &assistantpb.ClientMessage{
			SessionId: sessionID,
			UserId:    userID,
			Text:      text,
			Context:   map[string]string{},
			SentAt:    timestamppb.Now(),
		}

		if err := stream.Send(req); err != nil {
			log.Printf("failed to send message: %v", err)
			break
		}
	}

	fmt.Println("\nClient is shutting down.")
}

