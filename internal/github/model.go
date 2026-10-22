package github

import "time"

// Profile is the aggregate of data other packages render into cards.
type Profile struct {
	ID        string
	Login     string
	Name      string
	Bio       string
	AvatarURL string
	Company   string
	Location  string
	Website   string
	CreatedAt time.Time

	Followers int
	Following int
	// RepoCount is how many repos survived the fetch filters, so it tracks
	// -include-forks / -include-private / -include-org-repos rather than
	// GitHub's public-only publicRepos count.
	RepoCount int

	// Totals for the stats card.
	TotalStars                 int
	TotalForks                 int
	TotalCommits               int // last year, from contributionsCollection
	TotalCommitsAllTime        int // sum across contributionYears
	TotalPRs                   int
	TotalIssues                int
	TotalReviews               int
	TotalContributedTo         int
	TotalContributionsLastYear int // contributionCalendar.totalContributions + restrictedContributionsCount (last year)

	// Count of owned repos grouped by primary language, sorted desc by Value.
	ReposByLanguage []LangStat

	// Count of commits (last year, by this user) attributed to each repo's
	// primary language, sorted desc. Populated by FetchProductive.
	CommitsByLanguage []LangStat

	// Commit counts grouped by hour-of-day (0-23) in the configured timezone.
	// Productive is the last-year slice; ProductiveAllTime is the lifetime
	// slice derived from the same paginated commit history so we pay for
	// pagination once.
	Productive        [24]int
	ProductiveAllTime [24]int

	// Same pagination also feeds day-of-week histograms (index 0 = Sunday
	// to match time.Weekday). Last-year and all-time kept separately so the
	// weekday cards mirror the hour-of-day pair.
	Weekday        [7]int
	WeekdayAllTime [7]int

	// CommitsByLanguageAllTime is the lifetime counterpart of
	// CommitsByLanguage, computed from the same commit stream.
	CommitsByLanguageAllTime []LangStat

	// DailyContributions is the raw per-day contribution calendar covering
	// the most recent year. The area chart aggregates it into monthly
	// buckets; kept granular here so any downstream card can re-bin freely.
	DailyContributions []DailyContribution

	// DailyContributionsAllTime concatenates contribution calendars across
	// every year the user has been active (user.contributionYears), so the
	// all-time area chart can show history beyond the default 1-year window.
	DailyContributionsAllTime []DailyContribution

	// TopRepos are owned non-fork repos sorted by stargazer count desc,
	// populated by FetchProfile. Used for the profile's stars/forks totals
	// and the repos-per-language card.
	TopRepos []RepoInfo

	// SeedRepos are the repos where the user actually committed, unioned
	// across every contribution year via commitContributionsByRepository.
	// Used by FetchProductive so commit-history probes land where there is
	// signal instead of across the long tail of empty owned repos. Ordered
	// most recently active first, so a -top-repos cap keeps current work.
	SeedRepos []RepoInfo

	// ContributionYears lists every calendar year the user has been active
	// on GitHub, newest first. Used by FetchContributionsAllTime to iterate
	// per-year contributionsCollection queries.
	ContributionYears []int

	// UTCOffsetLabel is the configured timezone rendered as "UTC±N.NN" for
	// display on time-based cards (e.g. "UTC+7.00" for Asia/Saigon). Filled
	// by the CLI after loading -tz.
	UTCOffsetLabel string

	// WeekStart is the weekday used as row 0 on the heatmap and the first
	// bar on the productive-weekday chart. Defaults to time.Sunday to match
	// GitHub's own contribution calendar; -start-of-week on the CLI can flip
	// it to time.Monday (or any other weekday) without touching the data —
	// only the presentation order changes.
	WeekStart time.Weekday
}

// DailyContribution is a single day in the contributions calendar.
type DailyContribution struct {
	Date  time.Time
	Count int
}

// LangStat is one row in a language breakdown card. Value is repo count or
// commit count depending on which slice holds it.
type LangStat struct {
	Name  string
	Color string
	Value int64
}

// RepoInfo is the minimal repo summary used for downstream fetches. Owner is
// kept so the history query can target forks / repos belonging to other users.
type RepoInfo struct {
	Owner           string
	Name            string
	Stars           int
	IsPrivate       bool
	IsFork          bool
	PrimaryLanguage string
	PrimaryColor    string
	// Languages is the repo's language byte breakdown as reported by GitHub
	// linguist. Used to attribute each commit fractionally across all
	// languages the repo contains, rather than crediting only the primary.
	Languages []LangEdge
}

// LangEdge is a single entry in a repo's language-bytes breakdown.
type LangEdge struct {
	Name  string
	Color string
	Bytes int64
}

// repoNode is the GraphQL shape of one repository node; kept here because
// it's shared by the profile fetcher and the productive-time fetcher.
type repoNode struct {
	Name           string `json:"name"`
	IsPrivate      bool   `json:"isPrivate"`
	IsFork         bool   `json:"isFork"`
	StargazerCount int    `json:"stargazerCount"`
	ForkCount      int    `json:"forkCount"`
	// ViewerPermission is only requested by profileQuery, where it separates
	// org repos the user administers from ones they merely have access to.
	ViewerPermission string `json:"viewerPermission"`
	Owner            *struct {
		Login string `json:"login"`
	} `json:"owner"`
	PrimaryLanguage *struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"primaryLanguage"`
	Languages struct {
		Edges []struct {
			Size int64 `json:"size"`
			Node struct {
				Name  string `json:"name"`
				Color string `json:"color"`
			} `json:"node"`
		} `json:"edges"`
	} `json:"languages"`
}

// toRepoInfo converts the GraphQL shape into our public RepoInfo, defaulting
// owner to the given login if the node omits it (profile query doesn't
// request owner since it's implicit on user.repositories).
func (r repoNode) toRepoInfo(defaultOwner string) RepoInfo {
	info := RepoInfo{
		Owner:     defaultOwner,
		Name:      r.Name,
		Stars:     r.StargazerCount,
		IsPrivate: r.IsPrivate,
		IsFork:    r.IsFork,
	}
	if r.Owner != nil {
		info.Owner = r.Owner.Login
	}
	if r.PrimaryLanguage != nil {
		info.PrimaryLanguage = r.PrimaryLanguage.Name
		info.PrimaryColor = r.PrimaryLanguage.Color
	}
	for _, e := range r.Languages.Edges {
		info.Languages = append(info.Languages, LangEdge{
			Name:  e.Node.Name,
			Color: e.Node.Color,
			Bytes: e.Size,
		})
	}
	return info
}
