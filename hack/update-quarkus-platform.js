//import {App, Octokit} from "octokit";
//import xml2js from "xml2js";


const xml2js = require('xml2js');

const {Octokit} = require("octokit");
const https = require('https');
const fs = require('fs/promises');
const { spawn } = require('node:child_process');

const getLatestPlatform = () => {
    return new Promise((resolve, reject) => {
        const options = {
            hostname: 'code.quarkus.io',
            port: 443,
            path: '/api/platforms',
            method: 'GET',
        }
        https.request(options, res => {
            res.on('error', err => {
                reject(err)
            })
            let chunks = []
            res.on('data',chunk => {
                chunks.push(chunk)
            })
            res.on('end', () => {
                const data = JSON.parse(Buffer.concat(chunks).toString())
                resolve(data.platforms[0].streams[0].releases[0].quarkusCoreVersion)
            })
        }).end()
    });
}

const octokit = new Octokit({auth: process.env.GITHUB_TOKEN});
const [owner, repo] = process.env.GITHUB_REPOSITORY.split('/')

const prExists = async (pred) => {

    let page = 1
    const perPage = 10;

    while (true) {
        const resp = await octokit.rest.pulls.list({
            owner: owner,
            repo: repo,
            state: 'open',
            per_page: perPage,
            page: page
        })

        for (const e of resp.data) {
            if (pred(e)) {
                return true
            }
        }
        if (resp.data.length < perPage) {
            return false
        }
        page++
    }

}

const parseString = (text) => new Promise((resolve, reject) => {
    xml2js.parseString(text, {}, (err, res) => {
        if (err) {
            reject(err)
        }
        resolve(res)
    })
})

const platformFromPom = async (pomPath) => {
    const pomData = await fs.readFile(pomPath, { encoding: 'utf8' });
    const pom = await parseString(pomData)
    return pom.project.properties[0]['quarkus.platform.version'][0]
}

const cePomPath = "templates/quarkus/cloudevents/pom.xml"
const httpPomPath = "templates/quarkus/http/pom.xml"

const prepareBranch = async (branchName, prTitle) => {
    const script = `git config user.email "automation@knative.team" && \\
  git config user.name "Knative Automation" && \\
  git checkout -b "${branchName}" && \\
  make zz_filesystem_generated.go && \\
  git add "${cePomPath}" "${httpPomPath}" zz_filesystem_generated.go && \\
  git commit -m "${prTitle}" && \\
  git push --set-upstream origin "${branchName}"
`
    const subproc = spawn("sh", ["-c", script], {stdio: ['inherit', 'inherit', 'inherit']})

    return new Promise((resolve, reject) => {
        subproc.on('exit', code => {
            if (code === 0) {
                resolve()
                return
            }
            reject(new Error("cannot prepare branch: non-zero exit code"))
        })
    })
}

const updatePlatformInPom = async(pomPath, newPlatform) => {
    const pomData = await fs.readFile(pomPath, { encoding: 'utf8' });
    const newPomDate = pomData.replace(new RegExp('<quarkus.platform.version>[\\w.]+</quarkus.platform.version>', 'i'),
                    `<quarkus.platform.version>${newPlatform}</quarkus.platform.version>`)
    await fs.writeFile(pomPath, newPomDate)
}

const main = async () => {
    const latestPlatform = await getLatestPlatform()
    const prTitle = `chore: update Quarkus platform version to ${latestPlatform}`
    const branchName = `update-quarkus-platform-${latestPlatform}`
    const cePlatform = await platformFromPom(cePomPath)
    const httpPlatform = await platformFromPom(httpPomPath)

    if (cePlatform === latestPlatform && httpPlatform === latestPlatform) {
        console.log("Quarkus platform is up-to-date!")
        return
    }
    
    if (await prExists(({title}) => title === prTitle)) {
        console.log("The PR already exists!")
        return
    }

    await updatePlatformInPom(cePomPath, latestPlatform)
    await updatePlatformInPom(httpPomPath, latestPlatform)
    await prepareBranch(branchName, prTitle)
    await octokit.rest.pulls.create({
        owner: owner,
        repo: repo,
        title: prTitle,
        body: prTitle,
        base: 'main',
        head: `${owner}:${branchName}`,
    })
    console.log("The PR has been created!")

}

main().then(value => {
    console.log("OK!")
}).catch(reason => {
    console.log("ERROR: ", reason)
    process.exit(1)
})
