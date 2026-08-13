package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/store"
)

const maxCinemaDiscoveryQueryRunes = 100

type cinemaDiscovery interface {
	Cities(context.Context, string, *model.ProxyProfile) (checker.CinemaCityResult, error)
	Cinemas(context.Context, int64, string, *model.ProxyProfile) (checker.CinemaVenueResult, error)
	Movies(context.Context, int64, string, *model.ProxyProfile) (checker.CinemaMovieResult, error)
}

func WithCinemaDiscovery(discovery cinemaDiscovery) ServerOption {
	return func(server *Server) {
		server.cinemaDiscovery = discovery
	}
}

func defaultCinemaDiscovery() cinemaDiscovery {
	return checker.NewCinemaDiscovery()
}

func (s *Server) listCinemaCities(w http.ResponseWriter, r *http.Request) {
	query, err := cinemaDiscoveryQuery(r, false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	proxyProfile, err := s.resolveCinemaDiscoveryProxy(r, fields)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if len(fields) > 0 {
		writeError(w, r, validationProblem("请修正影院查询参数。", fields))
		return
	}
	result, err := s.cinemaDiscovery.Cities(r.Context(), query, proxyProfile)
	if err != nil {
		s.writeCinemaDiscoveryError(w, r, "city", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) searchCinemas(w http.ResponseWriter, r *http.Request) {
	cityID, query, proxyProfile, err := s.cinemaDiscoverySearchInput(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.cinemaDiscovery.Cinemas(r.Context(), cityID, query, proxyProfile)
	if err != nil {
		s.writeCinemaDiscoveryError(w, r, "cinema", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) searchCinemaMovies(w http.ResponseWriter, r *http.Request) {
	cityID, query, proxyProfile, err := s.cinemaDiscoverySearchInput(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.cinemaDiscovery.Movies(r.Context(), cityID, query, proxyProfile)
	if err != nil {
		s.writeCinemaDiscoveryError(w, r, "movie", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) cinemaDiscoverySearchInput(r *http.Request) (int64, string, *model.ProxyProfile, error) {
	fields := map[string]string{}
	cityID, err := positiveCinemaQueryID(r, "cityId")
	if err != nil {
		fields["cityId"] = "城市 ID 必须是正整数。"
	}
	query, queryErr := cinemaDiscoveryQuery(r, true)
	if queryErr != nil {
		if problem, ok := queryErr.(*problemError); ok {
			fields["q"] = problem.Fields["q"]
		} else {
			fields["q"] = queryErr.Error()
		}
	}

	proxyProfile, internalErr := s.resolveCinemaDiscoveryProxy(r, fields)
	if internalErr != nil {
		return 0, "", nil, internalErr
	}
	if len(fields) > 0 {
		return 0, "", nil, validationProblem("请修正影院查询参数。", fields)
	}
	return cityID, query, proxyProfile, nil
}

func (s *Server) resolveCinemaDiscoveryProxy(r *http.Request, fields map[string]string) (*model.ProxyProfile, error) {
	var proxyProfile *model.ProxyProfile
	if rawProxyID := strings.TrimSpace(r.URL.Query().Get("proxyId")); rawProxyID != "" {
		proxyID, parseErr := strconv.ParseInt(rawProxyID, 10, 64)
		if parseErr != nil || proxyID <= 0 {
			fields["proxyId"] = "代理 ID 必须是正整数。"
		} else {
			profile, lookupErr := s.store.GetProxyProfile(r.Context(), proxyID)
			if lookupErr != nil {
				if store.IsNotFound(lookupErr) {
					fields["proxyId"] = "请选择一个现有代理，或使用默认网络设置。"
				} else {
					return nil, fmt.Errorf("load cinema discovery proxy: %w", lookupErr)
				}
			} else {
				proxyProfile = &profile
			}
		}
	}
	return proxyProfile, nil
}

func cinemaDiscoveryQuery(r *http.Request, required bool) (string, error) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if required && query == "" {
		return "", validationProblem("请修正影院查询参数。", map[string]string{"q": "请输入搜索关键词。"})
	}
	if len([]rune(query)) > maxCinemaDiscoveryQueryRunes {
		return "", validationProblem("请修正影院查询参数。", map[string]string{"q": "搜索关键词不能超过 100 个字符。"})
	}
	return query, nil
}

func positiveCinemaQueryID(r *http.Request, key string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

func (s *Server) writeCinemaDiscoveryError(w http.ResponseWriter, r *http.Request, kind string, err error) {
	s.logger.Warn("cinema discovery request failed", "kind", kind, "error", err)
	writeError(w, r, &problemError{
		Status:  http.StatusBadGateway,
		Code:    "upstream_error",
		Message: "猫眼查询暂时不可用：" + err.Error(),
	})
}
