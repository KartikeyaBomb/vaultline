package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/KartikeyaBomb/vaultline/internal/chunkstore"
	"github.com/KartikeyaBomb/vaultline/internal/hash"
)

// newDebugCmd groups low-level tools for poking at a node's local state.
func newDebugCmd(a *app) *cobra.Command {
	debug := &cobra.Command{
		Use:   "debug",
		Short: "Low-level tools for inspecting local state",
	}
	chunk := &cobra.Command{
		Use:   "chunk",
		Short: "Inspect and modify the local chunk store",
	}
	chunk.AddCommand(
		chunkPutCmd(a),
		chunkGetCmd(a),
		chunkLsCmd(a),
		chunkVerifyCmd(a),
	)
	debug.AddCommand(chunk)
	return debug
}

func (a *app) openStore() (*chunkstore.Store, error) {
	return chunkstore.Open(a.cfg.DataDir, chunkstore.Options{})
}

func chunkPutCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "put <file|->",
		Short: "Store a whole file as one chunk and print its ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r io.Reader = cmd.InOrStdin()
			if args[0] != "-" {
				f, err := os.Open(args[0])
				if err != nil {
					return err
				}
				defer f.Close()
				r = f
			}
			data, err := io.ReadAll(io.LimitReader(r, chunkstore.MaxChunkSize+1))
			if err != nil {
				return err
			}
			s, err := a.openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			id := hash.Sum(data)
			written, err := s.Put(id, data)
			if err != nil {
				return err
			}
			a.log.Debug("put chunk", "id", id, "bytes", len(data), "new", written)
			fmt.Fprintln(cmd.OutOrStdout(), id)
			return nil
		},
	}
}

func chunkGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Write a chunk's contents to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := hash.Parse(args[0])
			if err != nil {
				return err
			}
			s, err := a.openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			data, err := s.Get(id)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func chunkLsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List chunk IDs in the store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			return s.Iterate(func(id hash.ChunkID) error {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), id)
				return err
			})
		},
	}
}

func chunkVerifyCmd(a *app) *cobra.Command {
	var quarantine bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Re-hash every chunk and report corrupt ones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			defer s.Close()

			// Collect first, so quarantining doesn't mutate the tree mid-walk.
			var corrupt []hash.ChunkID
			total := 0
			err = s.Iterate(func(id hash.ChunkID) error {
				total++
				if _, err := s.Get(id); errors.Is(err, chunkstore.ErrCorrupt) {
					a.log.Warn("corrupt chunk", "id", id, "err", err)
					corrupt = append(corrupt, id)
				} else if err != nil {
					return err
				}
				return nil
			})
			if err != nil {
				return err
			}
			if quarantine {
				for _, id := range corrupt {
					if err := s.Quarantine(id); err != nil {
						return err
					}
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "checked %d chunks, %d corrupt\n", total, len(corrupt))
			if len(corrupt) > 0 && !quarantine {
				return fmt.Errorf("%d corrupt chunks (rerun with --quarantine to move them aside)", len(corrupt))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&quarantine, "quarantine", false, "move corrupt chunks to quarantine/")
	return cmd
}
