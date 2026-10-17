// nrb2026 webapp の Go 版。配布された Rust 版 (KOBA789/nrb2026 の webapp/src/main.rs) を
// 同じ外部仕様のまま移植したもの。「正しいが意図的に遅い」初期実装で、
// current_count / status / last_joined_at は派生計算、性能目的の二次 index 無し、
// N+1、同期 webhook 送信。critical エラーを踏まないよう、トランザクション +
// FOR UPDATE で整合性は守る。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// users.credit_limit の初期値。全 user 共通の固定値。
const defaultCreditLimit = 60000

type dbConn struct {
	host     string
	port     string
	user     string
	password string
	database string
}

var (
	db       *sql.DB
	sqlDir   string
	dbConfig dbConn
)

func main() {
	dsn := getEnv("DATABASE_URL", "mysql://isucon:isucon@127.0.0.1:3306/nrb2026")
	var err error
	dbConfig, err = parseDBURL(dsn)
	if err != nil {
		log.Fatalf("DATABASE_URL parse: %v", err)
	}

	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = dbConfig.host + ":" + dbConfig.port
	cfg.User = dbConfig.user
	cfg.Passwd = dbConfig.password
	cfg.DBName = dbConfig.database
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.InterpolateParams = true
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	db, err = sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		log.Fatalf("connect to MySQL: %v", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	if err := db.Ping(); err != nil {
		log.Fatalf("connect to MySQL: %v", err)
	}

	sqlDir = getEnv("SQL_DIR", "../sql")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)

	// API は /api/ 配下。未定義の /api/* が SPA fallback に吸われないよう、明示的に 404 を返す。
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /api/initialize", initialize)
	mux.HandleFunc("POST /api/users", createUser)
	mux.HandleFunc("GET /api/tags", listTags)

	mux.HandleFunc("GET /api/me", withAuth(getMe))
	mux.HandleFunc("GET /api/campaigns", withAuth(listCampaigns))
	mux.HandleFunc("POST /api/campaigns", withAuth(createCampaign))
	mux.HandleFunc("GET /api/campaigns/{id}", withAuth(getCampaign))
	mux.HandleFunc("GET /api/campaigns/{id}/image", withAuth(getCampaignImage))
	mux.HandleFunc("POST /api/campaigns/{id}/join", withAuth(joinCampaign))
	mux.HandleFunc("POST /api/saved_searches", withAuth(createSavedSearch))
	mux.HandleFunc("GET /api/charges", withAuth(listCharges))

	// SPA (frontend の build 結果) を root に fallback 配信する。STATIC_DIR が無ければ配信しない。
	if dir := os.Getenv("STATIC_DIR"); dir != "" {
		index := filepath.Join(dir, "index.html")
		if st, err := os.Stat(index); err != nil || st.IsDir() {
			log.Fatalf("STATIC_DIR index.html not found: %s", index)
		}
		mux.Handle("/", spaHandler(dir, index))
	}

	port := getEnv("PORT", "8080")
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		log.Fatalf("PORT must be a valid u16: %v", err)
	}
	addr := "0.0.0.0:" + port
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func parseDBURL(dsn string) (dbConn, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return dbConn{}, err
	}
	c := dbConn{
		host:     u.Hostname(),
		port:     u.Port(),
		user:     u.User.Username(),
		database: strings.TrimPrefix(u.Path, "/"),
	}
	if c.host == "" {
		c.host = "127.0.0.1"
	}
	if c.port == "" {
		c.port = "3306"
	}
	c.password, _ = u.User.Password()
	return c, nil
}

// ファイルがあればそれを返し、無ければ index.html を 200 で返す (SPA のルータに任せる)。
func spaHandler(dir, index string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, index)
	})
}

// === error ===

type appError struct {
	status int
	msg    string
}

func (e *appError) Error() string { return e.msg }

var (
	errUnauthorized    = &appError{status: http.StatusUnauthorized}
	errBadRequest      = &appError{status: http.StatusBadRequest}
	errPaymentRequired = &appError{status: http.StatusPaymentRequired}
	errNotFound        = &appError{status: http.StatusNotFound}
	errConflict        = &appError{status: http.StatusConflict}
	errPayloadTooLarge = &appError{status: http.StatusRequestEntityTooLarge}
)

// エラーは本文無しでステータスだけ返す。
func writeError(w http.ResponseWriter, err error) {
	var ae *appError
	if errors.As(err, &ae) {
		w.WriteHeader(ae.status)
		return
	}
	log.Printf("internal error: %v", err)
	w.WriteHeader(http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

// リクエスト本文を JSON object として読む。壊れた JSON・object 以外・1 MiB 超は 400。
// 必須フィールドの欠落と null は、呼び出し側がポインタの nil で 400 にする。
func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 {
		return errBadRequest
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errBadRequest
	}
	if err := json.Unmarshal(body, v); err != nil {
		return errBadRequest
	}
	return nil
}

// === middleware ===

type ctxKey struct{}

func withAuth(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("X-User-ID")
		if h == "" {
			writeError(w, errUnauthorized)
			return
		}
		id, err := uuid.Parse(h)
		if err != nil {
			writeError(w, errUnauthorized)
			return
		}
		userID := id.String()
		var exists string
		err = db.QueryRowContext(r.Context(), "SELECT id FROM users WHERE id = ?", userID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, errUnauthorized)
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		next(w, r, userID)
	}
}

// === helpers ===

func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func fmtDT(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// JSON では "2024-05-01T12:00:00.000Z" の形 (ミリ秒) で出す。
type jsonTime time.Time

func (t jsonTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(fmtDT(time.Time(t)))
}

// price は 2000 以上 20000 以下。
func validatePrice(price int32) error {
	if price < 2000 || price > 20000 {
		return errBadRequest
	}
	return nil
}

// image (base64) を検査してデコードした bytes を返す。
//   - base64 デコード失敗 / 0 byte / JPEG magic 不一致 → 400
//   - デコード後 204_800 byte 超 → 413
func validateJPEGImageB64(b64 string) ([]byte, error) {
	// Go の decoder は改行を読み飛ばすので、先に弾いて RFC 4648 standard に揃える
	if strings.ContainsAny(b64, "\r\n") {
		return nil, errBadRequest
	}
	b, err := base64.StdEncoding.Strict().DecodeString(b64)
	if err != nil {
		return nil, errBadRequest
	}
	if len(b) == 0 {
		return nil, errBadRequest
	}
	if len(b) > 204_800 {
		return nil, errPayloadTooLarge
	}
	if len(b) < 3 || b[0] != 0xFF || b[1] != 0xD8 || b[2] != 0xFF {
		return nil, errBadRequest
	}
	return b, nil
}

// []string の JSON で、要素の null を 400 にするため []*string で受けて詰め直す。
func derefStrings(ps []*string) ([]string, error) {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p == nil {
			return nil, errBadRequest
		}
		out = append(out, *p)
	}
	return out, nil
}

// === DTOs ===

type campaignRes struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Price        int32            `json:"price"`
	GoalCount    int32            `json:"goal_count"`
	CurrentCount int32            `json:"current_count"`
	Tags         []string         `json:"tags"`
	Status       string           `json:"status"`
	CreatedAt    jsonTime         `json:"created_at"`
	LastJoinedAt *jsonTime        `json:"last_joined_at"`
	Participants []participantRes `json:"participants"`
}

type participantRes struct {
	UserID   string   `json:"user_id"`
	Name     string   `json:"name"`
	JoinedAt jsonTime `json:"joined_at"`
}

// === handlers ===

func initialize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NotificationWebhookURL *string `json:"notification_webhook_url"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.NotificationWebhookURL == nil {
		writeError(w, errBadRequest)
		return
	}

	if err := runMySQLFile(filepath.Join(sqlDir, "schema.sql")); err != nil {
		writeError(w, err)
		return
	}
	// 1500 件 + 画像入りの seed.sql があればそれを、無ければ 5 件だけの seed.base.sql を流す。
	seed := filepath.Join(sqlDir, "seed.sql")
	if _, err := os.Stat(seed); err != nil {
		seed = filepath.Join(sqlDir, "seed.base.sql")
	}
	if err := runMySQLFile(seed); err != nil {
		writeError(w, err)
		return
	}

	_, err := db.ExecContext(r.Context(),
		"INSERT INTO app_config (name, value) VALUES (?, ?) "+
			"ON DUPLICATE KEY UPDATE value = VALUES(value)",
		"notification_webhook_url", *req.NotificationWebhookURL)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func healthz(w http.ResponseWriter, r *http.Request) {
	var one int
	if err := db.QueryRowContext(r.Context(), "SELECT 1").Scan(&one); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// クライアントが途中で切断しても、流し込みは最後まで続ける (途中で止めると DB が半端な状態になる)。
func runMySQLFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	cmd := exec.Command("mysql",
		"-h", dbConfig.host,
		"-P", dbConfig.port,
		"-u", dbConfig.user,
		"--protocol=TCP",
		"--default-character-set=utf8mb4",
		dbConfig.database,
	)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+dbConfig.password)
	cmd.Stdin = f
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mysql %s: %w", path, err)
	}
	return nil
}

type userRes struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CreditLimit int32  `json:"credit_limit"`
}

func createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name *string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.Name == nil {
		writeError(w, errBadRequest)
		return
	}
	name := *req.Name
	if n := utf8.RuneCountInString(name); n == 0 || n > 100 {
		writeError(w, errBadRequest)
		return
	}
	id := uuid.NewString()
	_, err := db.ExecContext(r.Context(),
		"INSERT INTO users (id, name, credit_limit, created_at) VALUES (?, ?, ?, ?)",
		id, name, defaultCreditLimit, now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, userRes{ID: id, Name: name, CreditLimit: defaultCreditLimit})
}

type meRes struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CreditLimit int32  `json:"credit_limit"`
	CreditUsed  int32  `json:"credit_used"`
}

func getMe(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()
	var name string
	var creditLimit int32
	err := db.QueryRowContext(ctx, "SELECT name, credit_limit FROM users WHERE id = ?", userID).
		Scan(&name, &creditLimit)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, errUnauthorized)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}

	// credit_used: 自分が participants にいて current_count < goal_count な campaigns の price 合計。
	creditUsed, err := computeCreditUsed(ctx, db, userID)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, meRes{
		ID:          userID,
		Name:        name,
		CreditLimit: creditLimit,
		CreditUsed:  int32(creditUsed),
	})
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func queryStrings(ctx context.Context, q queryer, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func computeCreditUsed(ctx context.Context, q queryer, userID string) (int64, error) {
	cids, err := queryStrings(ctx, q, "SELECT campaign_id FROM campaign_participants WHERE user_id = ?", userID)
	if err != nil {
		return 0, err
	}
	var used int64
	for _, cid := range cids {
		var price, goalCount int32
		if err := q.QueryRowContext(ctx, "SELECT price, goal_count FROM campaigns WHERE id = ?", cid).
			Scan(&price, &goalCount); err != nil {
			return 0, err
		}
		var count int64
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM campaign_participants WHERE campaign_id = ?", cid).
			Scan(&count); err != nil {
			return 0, err
		}
		if int32(count) < goalCount {
			used += int64(price)
		}
	}
	return used, nil
}

func listTags(w http.ResponseWriter, r *http.Request) {
	names, err := queryStrings(r.Context(), db, "SELECT name FROM tags")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, names)
}

func listCampaigns(w http.ResponseWriter, r *http.Request, _ string) {
	ctx := r.Context()
	q := r.URL.Query()
	if len(q["tags"]) > 1 || len(q["sort"]) > 1 {
		writeError(w, errBadRequest)
		return
	}

	// tags フィルタ
	var tagFilter []string
	if s := q.Get("tags"); s != "" {
		parts := strings.Split(s, ",")
		if len(parts) > 3 {
			writeError(w, errBadRequest)
			return
		}
		seen := map[string]bool{}
		for _, p := range parts {
			if seen[p] {
				writeError(w, errBadRequest)
				return
			}
			seen[p] = true
		}
		for _, p := range parts {
			var id string
			err := db.QueryRowContext(ctx, "SELECT id FROM tags WHERE name = ?", p).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, errBadRequest)
				return
			}
			if err != nil {
				writeError(w, err)
				return
			}
		}
		tagFilter = parts
	}

	sortMode := "new"
	if q.Has("sort") {
		switch q.Get("sort") {
		case "active":
			sortMode = "active"
		case "new":
		default:
			writeError(w, errBadRequest)
			return
		}
	}

	// 全 campaign 行を hydrate する (LIMIT は最後)
	ids, err := queryStrings(ctx, db, "SELECT id FROM campaigns")
	if err != nil {
		writeError(w, err)
		return
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	all := []campaignRes{}
	for _, cid := range ids {
		c, err := hydrateCampaign(ctx, conn, cid)
		if err != nil {
			conn.Close()
			writeError(w, err)
			return
		}
		if c != nil {
			all = append(all, *c)
		}
	}
	conn.Close()

	// フィルタ (status=open + tag の AND)
	filtered := []campaignRes{}
	for _, c := range all {
		if c.Status != "open" {
			continue
		}
		have := map[string]bool{}
		for _, t := range c.Tags {
			have[t] = true
		}
		ok := true
		for _, want := range tagFilter {
			if !have[want] {
				ok = false
				break
			}
		}
		if ok {
			filtered = append(filtered, c)
		}
	}

	// active: last_joined_at (参加者 0 人なら created_at) の降順、new: created_at の降順
	if sortMode == "active" {
		key := func(c campaignRes) time.Time {
			if c.LastJoinedAt != nil {
				return time.Time(*c.LastJoinedAt)
			}
			return time.Time(c.CreatedAt)
		}
		sort.SliceStable(filtered, func(i, j int) bool {
			return key(filtered[i]).After(key(filtered[j]))
		})
	} else {
		sort.SliceStable(filtered, func(i, j int) bool {
			return time.Time(filtered[i].CreatedAt).After(time.Time(filtered[j].CreatedAt))
		})
	}

	if len(filtered) > 30 {
		filtered = filtered[:30]
	}
	writeJSON(w, http.StatusOK, filtered)
}

// tag 名を id に解決する。存在しない tag、同じ id への重複は 400。
func resolveTagIDs(ctx context.Context, names []string) ([]string, error) {
	tagIDs := []string{}
	for _, t := range names {
		var tid string
		err := db.QueryRowContext(ctx, "SELECT id FROM tags WHERE name = ?", t).Scan(&tid)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errBadRequest
		}
		if err != nil {
			return nil, err
		}
		for _, id := range tagIDs {
			if id == tid {
				return nil, errBadRequest
			}
		}
		tagIDs = append(tagIDs, tid)
	}
	return tagIDs, nil
}

func hasDuplicate(names []string) bool {
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			return true
		}
		seen[n] = true
	}
	return false
}

func createCampaign(w http.ResponseWriter, r *http.Request, _ string) {
	ctx := r.Context()
	var req struct {
		Name        *string    `json:"name"`
		Description *string    `json:"description"`
		Price       *int32     `json:"price"`
		GoalCount   *int32     `json:"goal_count"`
		Tags        *[]*string `json:"tags"`
		Image       *string    `json:"image"` // base64 の JPEG (canonical padding 必須)
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.Name == nil || req.Description == nil || req.Price == nil ||
		req.GoalCount == nil || req.Tags == nil || req.Image == nil {
		writeError(w, errBadRequest)
		return
	}
	tags, err := derefStrings(*req.Tags)
	if err != nil {
		writeError(w, err)
		return
	}

	if n := utf8.RuneCountInString(*req.Name); n == 0 || n > 100 {
		writeError(w, errBadRequest)
		return
	}
	if n := utf8.RuneCountInString(*req.Description); n == 0 || n > 1000 {
		writeError(w, errBadRequest)
		return
	}
	if err := validatePrice(*req.Price); err != nil {
		writeError(w, err)
		return
	}
	if *req.GoalCount < 2 || *req.GoalCount > 20 {
		writeError(w, errBadRequest)
		return
	}
	if len(tags) > 10 || hasDuplicate(tags) {
		writeError(w, errBadRequest)
		return
	}
	image, err := validateJPEGImageB64(*req.Image)
	if err != nil {
		writeError(w, err)
		return
	}
	tagIDs, err := resolveTagIDs(ctx, tags)
	if err != nil {
		writeError(w, err)
		return
	}

	id := uuid.NewString()
	t := now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO campaigns (id, name, description, price, goal_count, image, created_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, *req.Name, *req.Description, *req.Price, *req.GoalCount, image, t); err != nil {
		writeError(w, err)
		return
	}
	for _, tid := range tagIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO campaign_tags (campaign_id, tag_id, created_at) VALUES (?, ?, ?)",
			id, tid, t); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, err)
		return
	}

	res, err := hydrateCampaign(ctx, db, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if res == nil {
		writeError(w, errors.New("created campaign vanished"))
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// GET /api/campaigns/{id}/image
//
// 毎回 LONGBLOB を引いて SHA256 から ETag を作る。If-None-Match は読まず、常に 200 と本文を返す。
// Cache-Control も付けない。
func getCampaignImage(w http.ResponseWriter, r *http.Request, _ string) {
	var image []byte
	err := db.QueryRowContext(r.Context(), "SELECT image FROM campaigns WHERE id = ?", r.PathValue("id")).
		Scan(&image)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, errNotFound)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	sum := sha256.Sum256(image)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	w.WriteHeader(http.StatusOK)
	w.Write(image)
}

func getCampaign(w http.ResponseWriter, r *http.Request, _ string) {
	c, err := hydrateCampaign(r.Context(), db, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if c == nil {
		writeError(w, errNotFound)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func joinCampaign(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()
	campaignID := r.PathValue("id")
	// 本文は空の JSON object
	var req struct{}
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()

	// step 1: user 行を FOR UPDATE で取り、同じ user の与信の操作を直列化する。
	// campaign の FOR UPDATE だけだと、同じ user が別の campaign に並行して join したとき、
	// 両方が同じ credit_used を読んで credit_limit を超えられてしまう。
	var creditLimit int32
	if err := tx.QueryRowContext(ctx, "SELECT credit_limit FROM users WHERE id = ? FOR UPDATE", userID).
		Scan(&creditLimit); err != nil {
		writeError(w, err)
		return
	}

	// step 2: campaign 行を FOR UPDATE (goal_count と price)
	var goalCount, price int32
	err = tx.QueryRowContext(ctx, "SELECT goal_count, price FROM campaigns WHERE id = ? FOR UPDATE", campaignID).
		Scan(&goalCount, &price)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, errNotFound)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}

	// step 3: 現在の参加者数
	var before64 int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM campaign_participants WHERE campaign_id = ?", campaignID).
		Scan(&before64); err != nil {
		writeError(w, err)
		return
	}
	before := int32(before64)

	// step 4: 募集終了なら 409 (goal_count 超過を防ぐ)
	if before >= goalCount {
		writeError(w, errConflict)
		return
	}

	// step 5: 参加済みなら 409
	var dup string
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM campaign_participants WHERE campaign_id = ? AND user_id = ?",
		campaignID, userID).Scan(&dup)
	if err == nil {
		writeError(w, errConflict)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}

	// step 5.5: 与信の事前チェック。自分の参加行を足す前・この join による約定で枠が戻る前の
	// credit_used で判定する。
	beforeCreditUsed, err := computeCreditUsed(ctx, tx, userID)
	if err != nil {
		writeError(w, err)
		return
	}
	if int32(beforeCreditUsed)+price > creditLimit {
		writeError(w, errPaymentRequired)
		return
	}

	// step 6: 参加者を追加
	t := now()
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO campaign_participants (id, campaign_id, user_id, created_at) VALUES (?, ?, ?, ?)",
		uuid.NewString(), campaignID, userID, t); err != nil {
		writeError(w, err)
		return
	}
	after := before + 1

	// step 8: 残り 1 人になったら、通知先の user を集める (送信は commit 後)
	webhookUserIDs := []string{}
	if after == goalCount-1 {
		webhookUserIDs, err = queryStrings(ctx, tx,
			"SELECT DISTINCT ss.user_id "+
				"FROM saved_searches ss "+
				"JOIN saved_search_tags sst ON sst.saved_search_id = ss.id "+
				"LEFT JOIN campaign_tags ct ON ct.campaign_id = ? AND ct.tag_id = sst.tag_id "+
				"GROUP BY ss.id, ss.user_id "+
				"HAVING COUNT(*) = COUNT(ct.tag_id)",
			campaignID)
		if err != nil {
			writeError(w, err)
			return
		}
	}

	// step 9: 約定したら参加者全員に課金する (二重課金・課金漏れの防止)
	if after == goalCount {
		pids, err := queryStrings(ctx, tx, "SELECT id FROM campaign_participants WHERE campaign_id = ?", campaignID)
		if err != nil {
			writeError(w, err)
			return
		}
		for _, pid := range pids {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO charges (id, campaign_participant_id, created_at) VALUES (?, ?, ?)",
				uuid.NewString(), pid, t); err != nil {
				writeError(w, err)
				return
			}
		}
	}

	// step 9.5: レスポンスと webhook の中身は、並行 join とずれないようトランザクション内で作る
	res, err := hydrateCampaign(ctx, tx, campaignID)
	if err != nil {
		writeError(w, err)
		return
	}
	if res == nil {
		writeError(w, errors.New("modified campaign vanished"))
		return
	}

	// webhook の URL もトランザクション内で読んでおく (commit 後は DB に触らない)
	webhookURL := ""
	if len(webhookUserIDs) > 0 {
		err := tx.QueryRowContext(ctx, "SELECT value FROM app_config WHERE name = 'notification_webhook_url'").
			Scan(&webhookURL)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, err)
			return
		}
	}

	// step 10: commit してから外部 HTTP を送る
	if err := tx.Commit(); err != nil {
		writeError(w, err)
		return
	}

	// step 11: webhook を順に同期送信する (失敗は無視、リトライ無し)。image 系のフィールドは含めない
	if webhookURL != "" {
		for _, uid := range webhookUserIDs {
			body, _ := json.Marshal(map[string]any{
				"type":    "campaign_closing_soon",
				"user_id": uid,
				"campaign": map[string]any{
					"id":             res.ID,
					"name":           res.Name,
					"description":    res.Description,
					"price":          res.Price,
					"goal_count":     res.GoalCount,
					"current_count":  res.CurrentCount,
					"tags":           res.Tags,
					"status":         res.Status,
					"created_at":     res.CreatedAt,
					"last_joined_at": res.LastJoinedAt,
				},
			})
			resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(body))
			if err != nil {
				log.Printf("webhook send to %s for user %s: %v", webhookURL, uid, err)
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	writeJSON(w, http.StatusOK, res)
}

func createSavedSearch(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()
	var req struct {
		Tags *[]*string `json:"tags"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.Tags == nil {
		writeError(w, errBadRequest)
		return
	}
	tags, err := derefStrings(*req.Tags)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(tags) == 0 || len(tags) > 3 || hasDuplicate(tags) {
		writeError(w, errBadRequest)
		return
	}
	tagIDs, err := resolveTagIDs(ctx, tags)
	if err != nil {
		writeError(w, err)
		return
	}

	// 件数上限 10 を「user 行 FOR UPDATE → COUNT → INSERT」の 1 トランザクションで守る
	// (同じ user の並行 POST が、両方とも 10 未満を見て INSERT してしまうのを防ぐ)。
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	var locked string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id = ? FOR UPDATE", userID).Scan(&locked); err != nil {
		writeError(w, err)
		return
	}
	var count int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM saved_searches WHERE user_id = ?", userID).
		Scan(&count); err != nil {
		writeError(w, err)
		return
	}
	if count >= 10 {
		writeError(w, errConflict)
		return
	}

	ssID := uuid.NewString()
	t := now()
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO saved_searches (id, user_id, created_at) VALUES (?, ?, ?)",
		ssID, userID, t); err != nil {
		writeError(w, err)
		return
	}
	for _, tid := range tagIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO saved_search_tags (saved_search_id, tag_id, created_at) VALUES (?, ?, ?)",
			ssID, tid, t); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

type chargeRes struct {
	ID        string         `json:"id"`
	Amount    int32          `json:"amount"`
	Campaign  chargeCampaign `json:"campaign"`
	CreatedAt jsonTime       `json:"created_at"`
}

type chargeCampaign struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Price int32  `json:"price"`
}

func listCharges(w http.ResponseWriter, r *http.Request, userID string) {
	rows, err := db.QueryContext(r.Context(),
		"SELECT ch.id, ch.created_at, c.id, c.name, c.price "+
			"FROM charges ch "+
			"JOIN campaign_participants cp ON ch.campaign_participant_id = cp.id "+
			"JOIN campaigns c ON cp.campaign_id = c.id "+
			"WHERE cp.user_id = ? "+
			"ORDER BY ch.created_at DESC",
		userID)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	res := []chargeRes{}
	for rows.Next() {
		var c chargeRes
		var createdAt time.Time
		if err := rows.Scan(&c.ID, &createdAt, &c.Campaign.ID, &c.Campaign.Name, &c.Campaign.Price); err != nil {
			writeError(w, err)
			return
		}
		c.Amount = c.Campaign.Price
		c.CreatedAt = jsonTime(createdAt)
		res = append(res, c)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// === campaign の派生計算 (current_count / status / last_joined_at / tags / participants) ===
//
// トランザクション内では tx を渡して、同じ接続で計算する (並行 join とずれない)。

func hydrateCampaign(ctx context.Context, q queryer, id string) (*campaignRes, error) {
	// image (LONGBLOB) は引かない。画像は GET /api/campaigns/{id}/image で個別に返す。
	var c campaignRes
	var createdAt time.Time
	err := q.QueryRowContext(ctx,
		"SELECT id, name, description, price, goal_count, created_at FROM campaigns WHERE id = ?", id).
		Scan(&c.ID, &c.Name, &c.Description, &c.Price, &c.GoalCount, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.CreatedAt = jsonTime(createdAt)

	c.Tags, err = queryStrings(ctx, q,
		"SELECT t.name FROM campaign_tags ct JOIN tags t ON ct.tag_id = t.id WHERE ct.campaign_id = ?", c.ID)
	if err != nil {
		return nil, err
	}

	rows, err := q.QueryContext(ctx,
		"SELECT cp.user_id, u.name, cp.created_at "+
			"FROM campaign_participants cp JOIN users u ON cp.user_id = u.id "+
			"WHERE cp.campaign_id = ? ORDER BY cp.created_at ASC", c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	c.Participants = []participantRes{}
	for rows.Next() {
		var p participantRes
		var joinedAt time.Time
		if err := rows.Scan(&p.UserID, &p.Name, &joinedAt); err != nil {
			return nil, err
		}
		p.JoinedAt = jsonTime(joinedAt)
		c.Participants = append(c.Participants, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	c.CurrentCount = int32(len(c.Participants))
	if n := len(c.Participants); n > 0 {
		last := c.Participants[n-1].JoinedAt
		c.LastJoinedAt = &last
	}
	if c.CurrentCount >= c.GoalCount {
		c.Status = "closed"
	} else {
		c.Status = "open"
	}
	return &c, nil
}
