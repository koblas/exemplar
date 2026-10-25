package worker

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Runner claims (spec §2.4 C2). The App private key stays in control: the
// dispatcher mints exactly the installation tokens one job needs, seals
// them into a runner_claims row, and hands the runner a one-time nonce. The
// runner redeems the row once, works with clients for those repos and
// flavors only (ClaimClients), and clears the row when it exits.

// GrantToken is one installation token in a grant.
type GrantToken struct {
	// Repos the token covers: one, or several of one owner for FlavorEval.
	Repos        []string  `json:"repos"`
	Installation int64     `json:"installation"`
	Flavor       string    `json:"flavor"` // gh.Flavor.String()
	Token        string    `json:"token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Grant is the tokens one dispatched job may use.
type Grant struct {
	Tokens []GrantToken `json:"tokens"`
}

// expires is the grant's earliest token expiry (an hour away when empty:
// the runner refuses the job's repo itself).
func (g Grant) expires(now time.Time) time.Time {
	out := now.Add(time.Hour)
	for _, t := range g.Tokens {
		if t.ExpiresAt.Before(out) {
			out = t.ExpiresAt
		}
	}
	return out
}

// claimRetentionDays is how long a finished claim row (its tokens long
// cleared) is kept; rows whose tokens expired go at the next retention run.
const claimRetentionDays = 1

var (
	errClaimRefused = errors.New("worker: runner claim refused")
	errClaimSealed  = errors.New("worker: runner claim does not open with this key")
	errNotClaimed   = errors.New("worker: not in the runner's claim")
)

// Claims stores and redeems runner claims, sealed with AES-GCM.
type Claims struct {
	st   *store.Store
	aead cipher.AEAD
}

// NewClaims seals claims with key (envcfg.LabelRunnerClaims, or a key
// file of any length).
func NewClaims(st *store.Store, key []byte) (*Claims, error) {
	if st == nil || len(key) == 0 {
		return nil, fmt.Errorf("%w: claims need a store and a key", errMissingDependency)
	}
	k := sha256.Sum256(key)
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, fmt.Errorf("worker: claim cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("worker: claim cipher: %w", err)
	}
	return &Claims{st: st, aead: aead}, nil
}

// aad binds a sealed grant to its row, so it cannot be moved to another.
func aad(jobID int64) []byte {
	return binary.BigEndian.AppendUint64([]byte("runner-claim/"), uint64(jobID))
}

func nonceHash(nonce string) []byte {
	h := sha256.Sum256([]byte(nonce))
	return h[:]
}

// Mint stores g for t and returns the nonce the runner redeems it with. A
// retry's claim replaces the last attempt's.
func (c *Claims) Mint(ctx context.Context, t Task, g Grant) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("worker: claim nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	plain, err := json.Marshal(g)
	if err != nil {
		return "", fmt.Errorf("worker: encode grant: %w", err)
	}
	iv := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("worker: claim iv: %w", err)
	}
	sealed := c.aead.Seal(iv, iv, plain, aad(t.JobID))
	err = c.st.PutRunnerClaim(ctx, queries.PutRunnerClaimParams{
		JobID: t.JobID, Attempt: int32(t.Attempt), NonceHash: nonceHash(nonce), Tokens: sealed,
		ConfigVersion: t.ConfigVersion, ExpiresAt: pgtype.Timestamptz{Time: g.expires(time.Now()), Valid: true},
	})
	if err != nil {
		return "", fmt.Errorf("worker: store claim: %w", err)
	}
	return nonce, nil
}

// Redeem returns job id's grant and config version, once, for its nonce.
func (c *Claims) Redeem(ctx context.Context, id int64, nonce string) (Grant, string, error) {
	row, err := c.st.RedeemRunnerClaim(ctx, queries.RedeemRunnerClaimParams{JobID: id, NonceHash: nonceHash(nonce)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, "", fmt.Errorf("%w: job %d", errClaimRefused, id)
	}
	if err != nil {
		return Grant{}, "", fmt.Errorf("worker: redeem claim: %w", err)
	}
	// A grant that does not open (claim keys differ between control and
	// runner, or a key rotated mid-job) is an error River retries, with a
	// claim sealed under the current key; never a cancel.
	n := c.aead.NonceSize()
	if len(row.Tokens) < n {
		return Grant{}, "", fmt.Errorf("%w: job %d: sealed grant too short", errClaimSealed, id)
	}
	plain, err := c.aead.Open(nil, row.Tokens[:n], row.Tokens[n:], aad(id))
	if err != nil {
		return Grant{}, "", fmt.Errorf("%w: job %d: %w", errClaimSealed, id, err)
	}
	var g Grant
	if err := json.Unmarshal(plain, &g); err != nil {
		return Grant{}, "", fmt.Errorf("worker: decode grant: %w", err)
	}
	return g, row.ConfigVersion, nil
}

// Release clears job id's tokens; it may be called more than once.
func (c *Claims) Release(ctx context.Context, id int64) error {
	if err := c.st.ReleaseRunnerClaim(ctx, id); err != nil {
		return fmt.Errorf("worker: release claim: %w", err)
	}
	return nil
}

// ClaimClients is the GitHub of a runner: clients and clone tokens for the
// repos and flavors its claim grants, and nothing else.
type ClaimClients struct {
	Grant Grant
	// BaseURL overrides https://api.github.com/ (GHES).
	BaseURL string
}

// token finds the grant's token of flavor f for repo on installation
// inst. A worker-flavor read falls back to mode eval's read-only token.
func (c ClaimClients) token(inst int64, repo string, f gh.Flavor) (string, error) {
	find := func(f gh.Flavor) (string, bool) {
		for _, t := range c.Grant.Tokens {
			if t.Flavor == f.String() && t.Installation == inst &&
				slices.ContainsFunc(t.Repos, func(r string) bool { return strings.EqualFold(r, repo) }) {
				return t.Token, true
			}
		}
		return "", false
	}
	if tok, ok := find(f); ok {
		return tok, nil
	}
	if f == gh.FlavorWorker {
		if tok, ok := find(gh.FlavorEval); ok {
			return tok, nil
		}
	}
	return "", fmt.Errorf("%w: %s token for %s", errNotClaimed, f, repo)
}

func (c ClaimClients) client(inst int64, repo string, f gh.Flavor) (*github.Client, error) {
	tok, err := c.token(inst, repo, f)
	if err != nil {
		return nil, err
	}
	cl, err := gh.NewTokenClient(inst, tok, c.BaseURL, 0)
	if err != nil {
		return nil, fmt.Errorf("worker: %s client: %w", f, err)
	}
	return cl, nil
}

// Reader implements GitHub.
func (c ClaimClients) Reader(_ context.Context, inst int64, repo string) (*github.Client, error) {
	return c.client(inst, repo, gh.FlavorWorker)
}

// Gate implements GitHub.
func (c ClaimClients) Gate(_ context.Context, inst int64, repo string) (*github.Client, error) {
	return c.client(inst, repo, gh.FlavorGate)
}

// Poster implements GitHub.
func (c ClaimClients) Poster(_ context.Context, inst int64, repo string) (*github.Client, error) {
	return c.client(inst, repo, gh.FlavorPoster)
}

// CloneToken implements GitHub.
func (c ClaimClients) CloneToken(_ context.Context, inst int64, repo string) (string, error) {
	return c.token(inst, repo, gh.FlavorWorker)
}
