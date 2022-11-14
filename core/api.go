// Package core provides an API to include and use the GraphJin compiler with your own code.
// For detailed documentation visit https://graphjin.com
//
// Example usage:
/*
	package main

	import (
		"database/sql"
		"fmt"
		"time"
		"github.com/dosco/graphjin/core"
		_ "github.com/jackc/pgx/v4/stdlib"
	)

	func main() {
		db, err := sql.Open("pgx", "postgres://postgrs:@localhost:5432/example_db")
		if err != nil {
			log.Fatal(err)
		}

		gj, err := core.NewGraphJin(nil, db)
		if err != nil {
			log.Fatal(err)
		}

		query := `
			query {
				posts {
				id
				title
			}
		}`

		ctx = context.WithValue(ctx, core.UserIDKey, 1)

		res, err := gj.GraphQL(ctx, query, nil)
		if err != nil {
			log.Fatal(err)
		}

	}
*/
package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/dosco/graphjin/core/internal/graph"
	_log "log"
	"os"
	"sync"
	"sync/atomic"

	"github.com/chirino/graphql"
	"github.com/dop251/goja"
	"github.com/dosco/graphjin/core/internal/allow"
	"github.com/dosco/graphjin/core/internal/crypto"
	"github.com/dosco/graphjin/core/internal/psql"
	"github.com/dosco/graphjin/core/internal/qcode"
	"github.com/dosco/graphjin/core/internal/sdata"
	"github.com/dosco/graphjin/core/internal/util"
	"github.com/spf13/afero"
)

type contextkey int

// Constants to set values on the context passed to the NewGraphJin function
const (
	// Name of the authentication provider. Eg. google, github, etc
	UserIDProviderKey contextkey = iota

	// The raw user id (jwt sub) value
	UserIDRawKey

	// User ID value for authenticated users
	UserIDKey

	// User role if pre-defined
	UserRoleKey
)

// GraphJin struct is an instance of the GraphJin engine it holds all the required information like
// datase schemas, relationships, etc that the GraphQL to SQL compiler would need to do it's job.
type graphjin struct {
	conf        *Config
	db          *sql.DB
	log         *_log.Logger
	fs          afero.Fs
	dbtype      string
	dbinfo      *sdata.DBInfo
	schema      *sdata.DBSchema
	allowList   *allow.List
	encKey      [32]byte
	apq         apqCache
	queries     map[string]*queryComp
	roles       map[string]*Role
	roleStmt    string
	roleStmtMD  psql.Metadata
	rmap        map[string]resItem
	abacEnabled bool
	qc          *qcode.Compiler
	pc          *psql.Compiler
	ge          *graphql.Engine
	subs        sync.Map
	scripts     sync.Map
	prod        bool
}

type GraphJin struct {
	atomic.Value
}

type script struct {
	ReqFunc  reqFunc
	RespFunc respFunc
	vm       *goja.Runtime
	util.Once
}

type Option func(*graphjin) error

// NewGraphJin creates the GraphJin struct, this involves querying the database to learn its
// schemas and relationships
func NewGraphJin(conf *Config, db *sql.DB, options ...Option) (*GraphJin, error) {
	gj, err := newGraphJin(conf, db, nil, options...)
	if err != nil {
		return nil, err
	}

	g := &GraphJin{}
	g.Store(gj)

	if err := g.initDBWatcher(); err != nil {
		return nil, err
	}
	return g, nil
}

// newGraphJin helps with writing tests and benchmarks
func newGraphJin(conf *Config, db *sql.DB, dbinfo *sdata.DBInfo, options ...Option) (*graphjin, error) {
	if conf == nil {
		conf = &Config{Debug: true, DisableAllowList: true}
	}

	gj := &graphjin{
		conf:   conf,
		db:     db,
		dbinfo: dbinfo,
		log:    _log.New(os.Stdout, "", 0),
		prod:   conf.Production || os.Getenv("GO_ENV") == "production",
	}

	if err := gj.initAPQCache(); err != nil {
		return nil, err
	}

	//order matters, do not re-order the initializers
	if err := gj.initConfig(); err != nil {
		return nil, err
	}

	for _, op := range options {
		if err := op(gj); err != nil {
			return nil, err
		}
	}

	if err := gj.initFS(); err != nil {
		return nil, err
	}

	if err := gj.initDiscover(); err != nil {
		return nil, err
	}

	if err := gj.initResolvers(); err != nil {
		return nil, err
	}

	if err := gj.initSchema(); err != nil {
		return nil, err
	}

	if err := gj.initAllowList(); err != nil {
		return nil, err
	}

	if err := gj.initCompilers(); err != nil {
		return nil, err
	}

	if err := gj.initGraphQLEgine(); err != nil {
		return nil, err
	}

	if err := gj.prepareRoleStmt(); err != nil {
		return nil, err
	}

	if err := gj.initScripting(); err != nil {
		return nil, err
	}

	if conf.SecretKey != "" {
		sk := sha256.Sum256([]byte(conf.SecretKey))
		conf.SecretKey = ""
		gj.encKey = sk
	} else {
		gj.encKey = crypto.NewEncryptionKey()
	}

	return gj, nil
}

func OptionSetFS(fs afero.Fs) Option {
	return func(s *graphjin) error {
		s.fs = fs
		return nil
	}
}

type Error struct {
	Message string `json:"message"`
}

// Result struct contains the output of the GraphQL function this includes resulting json from the
// database query and any error information
type Result struct {
	op           qcode.QType
	name         string
	sql          string
	role         string
	cacheControl string
	Errors       []Error         `json:"errors,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
	Extensions   *extensions     `json:"extensions,omitempty"`
}

// ReqConfig is used to pass request specific config values to the GraphQLEx and SubscribeEx functions. Dynamic variables can be set here.
type ReqConfig struct {
	APQKey string
	Vars   map[string]interface{}
}

// GraphQL function is called on the GraphJin struct to convert the provided GraphQL query into an
// SQL query and execute it on the database. In production mode prepared statements are directly used
// and no query compiling takes places.
//
// In developer mode all names queries are saved into a file `allow.list` and in production mode only
// queries from this file can be run.
func (g *GraphJin) GraphQL(
	c context.Context,
	query string,
	vars json.RawMessage,
	rc *ReqConfig) (*Result, error) {

	var err error

	gj := g.Load().(*graphjin)

	ct := gcontext{
		Context: c,
		gj:      gj,
		rc:      rc,
	}

	if rc != nil && rc.APQKey != "" && query == "" {
		if v, ok := gj.apq.Get(rc.APQKey); ok {
			query = v.query
			ct.op = v.op
			ct.name = v.name
		} else {
			err = errors.New("PersistedQueryNotFound")
		}
	} else {
		ct.op, ct.name = qcode.GetQType(query)
	}

	res := &Result{
		op:   ct.op,
		name: ct.name,
	}

	if err != nil {
		res.Errors = []Error{{Message: err.Error()}}
		return res, err
	}

	if ct.op == qcode.QTSubscription {
		return res, errors.New("use 'core.Subscribe' for subscriptions")
	}

	if ct.op == qcode.QTMutation && gj.schema.DBType() == "mysql" {
		return res, errors.New("mysql: mutations not supported")
	}

	// use the chirino/graphql library for introspection queries
	// disabled when allow list is enforced
	if !gj.prod && ct.name == "IntrospectionQuery" {
		r := gj.ge.ServeGraphQL(&graphql.Request{Query: query})
		res.Data = r.Data

		if r.Error() != nil {
			res.Errors = []Error{{Message: r.Error().Error()}}
		}
		return res, r.Error()
	}

	var role string

	if v, ok := c.Value(UserRoleKey).(string); ok {
		role = v
	} else if c.Value(UserIDKey) != nil {
		role = "user"
	} else {
		role = "anon"
	}

	qreq := queryReq{
		op:    ct.op,
		name:  ct.name,
		query: []byte(query),
		vars:  vars,
	}
	qres, err := ct.execQuery(qreq, role)

	if err != nil {
		res.Errors = []Error{{Message: err.Error()}}
	}

	if qres.qc != nil {
		res.sql = qres.qc.st.sql
		if qres.qc.st.qc != nil {
			res.cacheControl = qres.qc.st.qc.Cache.Header
		}
	}

	res.Data = json.RawMessage(qres.data)
	res.role = qres.role

	return res, err
}

// Reload does database discover and reinitializes GraphJin.
func (g *GraphJin) Reload() error {
	gj := g.Load().(*graphjin)
	gjNew, err := newGraphJin(gj.conf, gj.db, nil)
	if err == nil {
		g.Store(gjNew)
	}
	return err
}

// IsProd return true for production mode or false for development mode
func (g *GraphJin) IsProd() bool {
	gj := g.Load().(*graphjin)
	return gj.prod
}

func (g *GraphJin) GetOpaPolicy(query string) (string, error) {
	op, err := graph.Parse([]byte(query), nil)
	if err != nil {
		return "", err
	}

	if len(op.Directives) > 0 {
		for _, directive := range op.Directives {
			if directive.Name != "opa" {
				continue
			}

			if len(directive.Args) == 0 {
				continue
			}

			arg := directive.Args[0]
			if arg.Name != "policy" {
				continue
			}

			policy := arg.Val.Val
			if len(policy) == 0 {
				continue
			}

			return policy, nil
		}
	}

	return "", errors.New("no policy found")
}

// Operation function return the operation type and name from the query.
// It uses a very fast algorithm to extract the operation without having to parse the query.
func Operation(query string) (OpType, string) {
	qt, name := qcode.GetQType(query)
	return OpType(qt), name
}
