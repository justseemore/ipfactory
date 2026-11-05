module github.com/justseemore/ipfactory

go 1.26.0

// GeoCN（mmdb）需要它；awdb / ipgeo 改为按最新版解析，可由 go mod tidy 补齐。
require github.com/oschwald/maxminddb-golang v1.13.1

require (
	github.com/justseemore/awdb v0.0.0-20261105093557-8880e0091f24
	github.com/justseemore/ipgeo v0.0.0-20261105092903-40d37fec27aa
)

require (
	github.com/lionsoul2014/ip2region/binding/golang v0.0.0-20261028042204-6a276cc7c4f9 // indirect
	golang.org/x/sys v0.49.0 // indirect
)
