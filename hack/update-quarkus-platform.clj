(ns qbot
  (:require
    [clojure.java.io :as io]
    [org.httpkit.client :as client]
    [cheshire.core :as json]
    [clojure.data.xml :as xml]
    [clojure.string :as str]
    [clojure.java.shell :as shell]
    )
  (:import (java.io InputStream)))

(defn get-latest-platform
  "Get latest Quarkus platform version by accessing https://code.quarkus.io/api/platforms."
  []
  (-> (slurp "https://code.quarkus.io/api/platforms")
      (json/parse-string keyword)
      (:platforms)
      (first)
      (:streams)
      (first)
      (:releases)
      (first)
      (:quarkusCoreVersion)))

(defn get-current-platform
  "Gets current platform from pom.xml"
  [pom-path]
  (letfn [(search [{:keys [tag content]}]
            (if (.endsWith (str tag) "quarkus.platform.version")
              (first content)
              (->> content
                   (map #(search %))
                   (filter some?)
                   (first))))]
    (with-open [r (io/reader pom-path)]
      (search (xml/parse r)))))

(defn update-platform
  "Updates Quarkus platform in pom.xml. Naïve textual substitution is employed.
  XML parsing would be overkill here, and it could change formatting."
  [new-platform pom-path]
  (let [pom-updated (str/replace
                      (slurp pom-path)
                      #"<quarkus.platform.version>[\w.]+</quarkus.platform.version>"
                      (str "<quarkus.platform.version>" new-platform "</quarkus.platform.version>"))]
    (spit pom-path pom-updated)))

(def ce-pom "templates/quarkus/cloudevents/pom.xml")
(def http-pom "templates/quarkus/http/pom.xml")

(defn prepare-branch
  "Creates branch with updated platform and pushes it."
  [branch-name title]
  (let [script (format "git config user.email \"automation@knative.team\" && \\
  git config ser.name \"Knative Automation\" && \\
  git checkout -b '%s' && \\
  make zz_filesystem_generated.go && \\
  git add '%s' '%s' zz_filesystem_generated.go && \\
  git commit -m '%s' && \\
  git push --set-upstream origin '%s'"branch-name ce-pom http-pom title branch-name)
        {:keys [exit out err]} (shell/sh "sh" "-c" script)]
    (when (not= exit 0)
      (throw (ex-info "cannot prepare branch" {:err err :out out :command script})))))

(def ^:dynamic *gh-token* nil)

(defn gh-api-request
  "Helper function for making GitHub API requests.
  It JSON-fy request/response body, throws error and injects auth token."
  [method url opts]
  (let [opts (merge {:url url :method method :as :stream} opts)
        opts (update opts :headers (fn [headers]
                                     (merge {"Accept"        "application/vnd.github+json"
                                             "Authorization" (str "token " *gh-token*)}
                                            headers)))
        opts (update opts :body (fn [body]
                                  (if (and (some? body)
                                           (not (instance? InputStream body))
                                           (not (instance? String body)))
                                    (json/generate-string body)
                                    body)))
        {:keys [body status error]} @(client/request opts)
        body (with-open [r (io/reader body)]
               (json/parse-stream-strict r keyword))]

    (cond
      (some? error)
      (throw error)
      ;;
      (<= 200 status 299)
      body
      ;;
      :else
      (throw (ex-info "request error" body)))))

(defn list-prs
  [{:keys [repo state page per-page]}]
  (gh-api-request :get
                  (str "https://api.github.com/repos/" repo "/pulls")
                  {:query-params {:state    state
                                  :page     page
                                  :per_page per-page}}))

(defn pr-open?
  "Checks whether the PR with given title is already opened in repository."
  [{:keys [repo title]}]
  (let [all-prs (->> (range)
                     (drop 1)
                     (map #(list-prs {:repo repo, :state :open, :page %, :per-page 30}))
                     (take-while #(not (empty? %)))
                     (apply concat))]
    (->> all-prs
         (filter #(= (:title %) title))
         (first)
         (some?))))

(defn create-pr
  "Creates a PR."
  [{:keys [repo title body head base]}]
  (gh-api-request :post
                  (str "https://api.github.com/repos/" repo "/pulls")
                  {:body {:title title
                          :body  body
                          :base  base
                          :head  head}})
  )

(defn -main [& args]
  (binding [*gh-token* (System/getenv "GITHUB_TOKEN")]
    (let [latest-platform (get-latest-platform)
          branch-name (str "update-quarkus-platform-" latest-platform)
          title (str "chore: update Quarkus platform version to " latest-platform)
          gh-repo-owner (System/getenv "GITHUB_REPOSITORY_OWNER")
          gh-repo (System/getenv "GITHUB_REPOSITORY")]
      (cond
        (= (get-latest-platform)
           (get-current-platform ce-pom)
           (get-current-platform http-pom))
        (println "Quarkus platform is up-to-date!")
        ;;
        (pr-open? {:repo  gh-repo
                   :title title})
        (println "The PR is already opened!")
        ;;
        :else
        (do (update-platform latest-platform ce-pom)
            (update-platform latest-platform http-pom)
            (prepare-branch branch-name title)
            (create-pr {:repo  gh-repo
                        :title title
                        :body  title
                        :base  "main"
                        :head  (str gh-repo-owner ":" branch-name)})
            (println "The PR has been created!"))))))

(-main)
