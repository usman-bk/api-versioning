# api-versioning

_Built with AI assistance._

One endpoint that answers four versions of itself, because the clients are
apps on phones nobody can force to update and some of them will not be
updated for a year.

The handler builds one shape. Everything older is produced by a chain of
small functions, each of which undoes exactly one change and has not been
touched since the day it was written.

Go 1.23, no dependencies outside the standard library.

```
internal/apiver/     which version a request wants, and how long it has left
internal/migrate/    turning the current shape into an older one
internal/handler/    the endpoint, the chain, and the golden files
cmd/versiond/
```

## A chain, not a branch

The thing most codebases do first is a struct per version and a branch in
every handler. It works for two versions and collapses at four: the handler
becomes a switch nobody dares touch, a bug fixed in the current shape is
fixed in one branch and not the other three, and adding a version means
editing every endpoint.

Here, each version contributes one function that converts the version above
it down to itself.

    v4 ──(warranty became an object, battery added)──▶ v3
    v3 ──(images gained a width, so became objects)──▶ v2
    v2 ──(price gained a currency, stock became an object)──▶ v1

Serving v1 is three small functions rather than one four-way branch. Adding
v5 is one new function and no change to anything that already works.

The cost is that migrations run per request on a decoded map rather than on a
struct. `BenchmarkThreeStepChain` measures the full v4 to v1 conversion plus
re-encoding at **3.9 µs and 39 allocations**, which is not the thing to
optimise on a request that also does a database read.

A map and not a typed struct per version, because a struct per version is a
type that has to keep compiling forever, and all a migration does is add,
remove, rename and reshape fields.

## Golden files are the contract

A migration is correct if the bytes an old client receives are the bytes it
received before. The only way to know that is to have written them down, so
`internal/handler/testdata/` holds the exact response for every live version.

    UPDATE_GOLDEN=1 go test ./internal/handler/

A change that alters one of those files is either a bug or a decision
somebody is making deliberately, and either way the diff is in the pull
request where it can be argued about.

v1, as the oldest client still believes the world looks:

    {
      "grade": "excellent",
      "id": "lst_1",
      "images": ["https://img.example.com/a/720.avif", "..."],
      "in_stock": true,
      "price": 48900,
      "stock_count": 2,
      "warranty_months": 12
    }

The same listing at v4:

    {
      "battery_health_pct": 93,
      "images": [{"url": "...", "width": 720}],
      "price": {"cents": 48900, "currency": "EUR"},
      "stock": {"available": true, "count": 2},
      "warranty": {"extended": false, "months": 12}
    }

Nothing from a later version may leak into an earlier response. Most clients
ignore a field they do not recognise, and most is not all: a strict decoder
fails on it, and the clients that use one are generated from a schema, which
is the kind an app usually has.

## A header, not a path prefix

`X-API-Version: v2`.

`/v1/` and `/v2/` means every route exists twice in the router, the two
drift, and a client cannot move one endpoint at a time. A header keeps one
route and makes the version a property of the request, which is what it is.

Lenient on spelling, strict on meaning. `2`, `v2`, `V2` and ` v2 ` all work,
because three clients will send three spellings whatever the documentation
says and none of that is worth failing a request over. `latest` does not
work, because it means the client has no idea which shape it is about to
receive, which is the whole problem.

## The default is the oldest version, not the newest

A request with no version header gets v1.

A caller that does not send a version is either very old or is not an app at
all. Giving it the newest shape is the one choice guaranteed to break it.
Defaulting forward looks tidy and breaks exactly the callers who had no idea
they were making a choice.

The default is computed from the versions that still answer, not fixed at
startup. Held as a fixed field it returned a retired version from the morning
the sunset fell, and every unversioned request was answered 410 by a server
that had four working versions. The sunset test found that, and it is in the
history.

## Deprecation is said out loud, repeatedly

The only lever a server has over an app it cannot update is telling it early
and often, so a deprecated version carries it on every response:

    Deprecation: true
    Sunset: Fri, 22 Jan 2027 19:23:10 GMT
    Warning: 299 - "v1 is deprecated, the current version is v4. price was a
             bare integer in minor units with no currency. It stops answering
             in 91 days"

`Deprecation` and `Sunset` are the ones in RFC 8594 and RFC 9745, rather than
a header only this service emits, so tooling people already have can read
them.

A version retires when its sunset date passes, whether or not anybody edited
the list. A sunset nobody enforces is a suggestion.

## Errors the app can tell apart

| | |
|---|---|
| 410 `version_retired` | this version is gone. The app shows "update to continue" |
| 400 `version_unknown` | the version never existed. That is a bug to report |
| 404 | no such listing |

410 and not 400 for a retired version, because the client did nothing wrong
when it was built, and the two cases want different screens. Both errors name
the current version and list the live ones, so the answer is in the response
rather than in documentation somebody has to find.

## The service refuses to start with a hole in the chain

A missing migration means a request for that version is answered with a newer
shape, silently, and nobody finds out until a client reports it.

`handler.New` checks that every live version can actually be produced and
returns an error if not, so the deploy stops instead of the bug shipping.

## Running it

    go run ./cmd/versiond

    curl -s localhost:8080/listings/lst_1 -H 'X-API-Version: v1'
    curl -s localhost:8080/versions

    go test ./...     38 cases, no database and no network

## Not in here

No request body migration. These are read endpoints, and a write needs the
chain run the other way, upwards from what the client sent to the current
shape, which is a second set of functions.

No per field deprecation. A version is deprecated or it is not, and a field
that is going away gets a new version.

No telemetry on which versions are actually in use, which is the thing that
decides whether v1 can be retired on schedule. That belongs with the metrics
stack rather than here, and without it the sunset date is a guess.
