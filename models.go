package main

type BitbucketEvent struct {
	Actor struct {
		Active       bool   `json:"active"`
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
		ID           int64  `json:"id"`
		Links        struct {
			Self []struct {
				Href string `json:"href"`
			} `json:"self"`
		} `json:"links"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Type string `json:"type"`
	} `json:"actor"`
	Date        string `json:"date"`
	EventKey    string `json:"eventKey"`
	Participant struct {
		Approved           bool   `json:"approved"`
		LastReviewedCommit string `json:"lastReviewedCommit"`
		Role               string `json:"role"`
		Status             string `json:"status"`
		User               struct {
			Active       bool   `json:"active"`
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
			ID           int64  `json:"id"`
			Links        struct {
				Self []struct {
					Href string `json:"href"`
				} `json:"self"`
			} `json:"links"`
			Name string `json:"name"`
			Slug string `json:"slug"`
			Type string `json:"type"`
		} `json:"user"`
	} `json:"participant"`
	PreviousStatus string                    `json:"previousStatus"`
	PullRequest    BitbucketEventPullRequest `json:"pullRequest"`
}

type BitbucketEventPullRequest struct {
	Author struct {
		Approved bool   `json:"approved"`
		Role     string `json:"role"`
		Status   string `json:"status"`
		User     struct {
			Active       bool   `json:"active"`
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
			ID           int64  `json:"id"`
			Links        struct {
				Self []struct {
					Href string `json:"href"`
				} `json:"self"`
			} `json:"links"`
			Name string `json:"name"`
			Slug string `json:"slug"`
			Type string `json:"type"`
		} `json:"user"`
	} `json:"author"`
	Closed      bool   `json:"closed"`
	CreatedDate int64  `json:"createdDate"`
	Description string `json:"description"`
	Draft       bool   `json:"draft"`
	FromRef     struct {
		DisplayID    string `json:"displayId"`
		ID           string `json:"id"`
		LatestCommit string `json:"latestCommit"`
		Repository   struct {
			Archived    bool   `json:"archived"`
			Forkable    bool   `json:"forkable"`
			HierarchyID string `json:"hierarchyId"`
			ID          int64  `json:"id"`
			Links       struct {
				Clone []struct {
					Href string `json:"href"`
					Name string `json:"name"`
				} `json:"clone"`
				Self []struct {
					Href string `json:"href"`
				} `json:"self"`
			} `json:"links"`
			Name    string `json:"name"`
			Project struct {
				ID    int64  `json:"id"`
				Key   string `json:"key"`
				Links struct {
					Self []struct {
						Href string `json:"href"`
					} `json:"self"`
				} `json:"links"`
				Name   string `json:"name"`
				Public bool   `json:"public"`
				Type   string `json:"type"`
			} `json:"project"`
			Public        bool   `json:"public"`
			ScmID         string `json:"scmId"`
			Slug          string `json:"slug"`
			State         string `json:"state"`
			StatusMessage string `json:"statusMessage"`
		} `json:"repository"`
		Type string `json:"type"`
	} `json:"fromRef"`
	ID    int64 `json:"id"`
	Links struct {
		Self []struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
	Locked       bool                                `json:"locked"`
	Open         bool                                `json:"open"`
	Participants []any                               `json:"participants"`
	Reviewers    []BitbucketEventPullRequestReviewer `json:"reviewers"`
	State        string                              `json:"state"`
	Title        string                              `json:"title"`
	ToRef        struct {
		DisplayID    string `json:"displayId"`
		ID           string `json:"id"`
		LatestCommit string `json:"latestCommit"`
		Repository   struct {
			Archived    bool   `json:"archived"`
			Forkable    bool   `json:"forkable"`
			HierarchyID string `json:"hierarchyId"`
			ID          int64  `json:"id"`
			Links       struct {
				Clone []struct {
					Href string `json:"href"`
					Name string `json:"name"`
				} `json:"clone"`
				Self []struct {
					Href string `json:"href"`
				} `json:"self"`
			} `json:"links"`
			Name    string `json:"name"`
			Project struct {
				ID    int64  `json:"id"`
				Key   string `json:"key"`
				Links struct {
					Self []struct {
						Href string `json:"href"`
					} `json:"self"`
				} `json:"links"`
				Name   string `json:"name"`
				Public bool   `json:"public"`
				Type   string `json:"type"`
			} `json:"project"`
			Public        bool   `json:"public"`
			ScmID         string `json:"scmId"`
			Slug          string `json:"slug"`
			State         string `json:"state"`
			StatusMessage string `json:"statusMessage"`
		} `json:"repository"`
		Type string `json:"type"`
	} `json:"toRef"`
	UpdatedDate int64 `json:"updatedDate"`
	Version     int64 `json:"version"`
}

type BitbucketEventPullRequestReviewer struct {
	Approved           bool   `json:"approved"`
	LastReviewedCommit string `json:"lastReviewedCommit"`
	Role               string `json:"role"`
	Status             string `json:"status"`
	User               struct {
		Active       bool   `json:"active"`
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
		ID           int64  `json:"id"`
		Links        struct {
			Self []struct {
				Href string `json:"href"`
			} `json:"self"`
		} `json:"links"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Type string `json:"type"`
	} `json:"user"`
}

type BitbucketPullRequest struct {
	ID          int64  `json:"id"`
	Version     int64  `json:"version"`
	Title       string `json:"title"`
	Description string `json:"description"`
	FromRef     struct {
		ID string `json:"id"`
	} `json:"fromRef"`
	ToRef struct {
		ID string `json:"id"`
	} `json:"toRef"`
	Reviewers []BitbucketPullRequestReviewer `json:"reviewers"`
}

type BitbucketPullRequestReviewer struct {
	User struct {
		Name string `json:"name"`
	} `json:"user"`
}

type BitbucketPullRequestResponse struct {
	Errors []struct {
		Context        string `json:"context"`
		Message        string `json:"message"`
		ExceptionName  string `json:"exceptionName"`
		ReviewerErrors []struct {
			Context string `json:"context"`
			Message string `json:"message"`
		} `json:"reviewerErrors"`
		ValidReviewers []struct {
			User struct {
				Name         string `json:"name"`
				EmailAddress string `json:"emailAddress"`
				Active       bool   `json:"active"`
				DisplayName  string `json:"displayName"`
				ID           int    `json:"id"`
				Slug         string `json:"slug"`
				Type         string `json:"type"`
				Links        struct {
					Self []struct {
						Href string `json:"href"`
					} `json:"self"`
				} `json:"links"`
			} `json:"user"`
		} `json:"validReviewers"`
	} `json:"errors"`
}

type BitbucketUsers struct {
	Values []struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
		Permission string `json:"permission"`
	} `json:"values"`
}

type Repo struct {
	Owner string `json:"owner"`
}

type BitbucketDefaultReviewer struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}
