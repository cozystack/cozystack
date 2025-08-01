# Contributor Ladder Template

* [Contributor Ladder](#contributor-ladder-template)
    * [Community Participant](#community-participant)
    * [Contributor](#contributor)
    * [Reviewer](#reviewer)
    * [Maintainer](#maintainer)
* [Inactivity](#inactivity)
* [Involuntary Removal](#involuntary-removal-or-demotion)
* [Stepping Down/Emeritus Process](#stepping-downemeritus-process)
* [Contact](#contact)


## Contributor Ladder

Hello! We are excited that you want to learn more about our project contributor ladder! This contributor ladder outlines the different contributor roles within the project, along with the responsibilities and privileges that come with them. Community members generally start at the first levels of the "ladder" and advance up it as their involvement in the project grows.  Our project members are happy to help you advance along the contributor ladder.

Each of the contributor roles below is organized into lists of three types of things. "Responsibilities" are things that a contributor is expected to do. "Requirements" are qualifications a person needs to meet to be in that role, and "Privileges" are things contributors on that level are entitled to.


### Community Participant
Description: A Community Participant engages with the project and its community, contributing their time, thoughts, etc. Community participants are usually users who have stopped being anonymous and started being active in project discussions.

* Responsibilities:
    * Must follow the [CNCF CoC](https://github.com/cncf/foundation/blob/main/code-of-conduct.md)
* How users can get involved with the community:
    * Participating in community discussions
    * Helping other users
    * Submitting bug reports
    * Commenting on issues
    * Trying out new releases
    * Attending community events


### Contributor
Description: A Contributor contributes directly to the project and adds value to it. Contributions need not be code. People at the Contributor level may be new contributors, or they may only contribute occasionally.

* Responsibilities include:
    * Follow the [CNCF CoC](https://github.com/cncf/foundation/blob/main/code-of-conduct.md)
    * Follow the project [contributing guide] (https://github.com/cozystack/cozystack/blob/main/CONTRIBUTING.md)
* Requirements (one or several of the below):
    * Report and sometimes resolve issues
    * Occasionally submit PRs
    * Contribute to the documentation
    * Show up at meetings, takes notes
    * Answer questions from other community members
    * Submit feedback on issues and PRs
    * Test releases and patches and submit reviews
    * Run or helps run events
    * Promote the project in public
    * Help run the project infrastructure
* Privileges:
    * Invitations to contributor events
    * Eligible to become a Maintainer


### Reviewer
Description: A Reviewer has responsibility for specific code, documentation, test, or other project areas. They are collectively responsible, with other Reviewers, for reviewing all changes to those areas and indicating whether those changes are ready to merge. They have a track record of contribution and review in the project.

Reviewers are responsible for a "specific area." This can be a specific code directory, driver, chapter of the docs, test job, event, or other clearly-defined project component that is smaller than an entire repository or subproject. Most often it is one or a set of directories in one or more Git repositories. The "specific area" below refers to this area of responsibility.

Reviewers have all the rights and responsibilities of a Contributor, plus:

* Responsibilities include:
    * Continues to contribute regularly, as demonstrated by having at least 15 PRs a year, as demonstrated by [Cozystack devstats](https://cozystack.devstats.cncf.io).
    * Following the reviewing guide
    * Reviewing most Pull Requests against their specific areas of responsibility
    * Reviewing at least 40 PRs per year
    * Helping other contributors become reviewers
* Requirements:
    * Must have successful contributions to the project, including at least one of the following:
        * 10 accepted PRs,
        * Reviewed 20 PRs,
        * Resolved and closed 20 Issues,
        * Become responsible for a key project management area,
        * Or some equivalent combination or contribution
    * Must have been contributing for at least 6 months
    * Must be actively contributing to at least one project area
    * Must have two sponsors who are also Reviewers or Maintainers, at least one of whom does not work for the same employer
    * Has reviewed, or helped review, at least 20 Pull Requests
    * Has analyzed and resolved test failures in their specific area
    * Has demonstrated an in-depth knowledge of the specific area
    * Commits to being responsible for that specific area
    * Is supportive of new and occasional contributors and helps get useful PRs in shape to commit
* Additional privileges:
    * Has GitHub or CI/CD rights to approve pull requests in specific directories
    * Can recommend and review other contributors to become Reviewers
    * May be assigned Issues and Reviews
    * May give commands to CI/CD automation
    * Can recommend other contributors to become Reviewers


The process of becoming a Reviewer is:
1. The contributor is nominated by opening a PR against the appropriate repository, which adds their GitHub username to the OWNERS file for one or more directories.
2. At least two members of the team that owns that repository or main directory, who are already Approvers, approve the PR.


### Maintainer
Description: Maintainers are very established contributors who are responsible for the entire project. As such, they have the ability to approve PRs against any area of the project, and are expected to participate in making decisions about the strategy and priorities of the project.

A Maintainer must meet the responsibilities and requirements of a Reviewer, plus:

* Responsibilities include:
    * Reviewing at least 40 PRs per year, especially PRs that involve multiple parts of the project
    * Mentoring new Reviewers
    * Writing refactoring PRs
    * Participating in CNCF maintainer activities
    * Determining strategy and policy for the project
    * Participating in, and leading, community meetings
* Requirements
    * Experience as a Reviewer for at least 6 months
    * Demonstrates a broad knowledge of the project across multiple areas
    * Is able to exercise judgment for the good of the project, independent of their employer, friends, or team
    * Mentors other contributors
    * Can commit to spending at least 10 hours per month working on the project
* Additional privileges:
    * Approve PRs to any area of the project
    * Represent the project in public as a Maintainer
    * Communicate with the CNCF on behalf of the project
    * Have a vote in Maintainer decision-making meetings
    

Process of becoming a maintainer:
1. Any current Maintainer may nominate a current Reviewer to become a new Maintainer, by opening a PR against the root of the cozystack repository adding the nominee as an Approver in the [MAINTAINERS](https://github.com/cozystack/cozystack/blob/main/MAINTAINERS.md) file.
2. The nominee will add a comment to the PR testifying that they agree to all requirements of becoming a Maintainer.
3. A majority of the current Maintainers must then approve the PR.


## Inactivity
It is important for contributors to be and stay active to set an example and show commitment to the project. Inactivity is harmful to the project as it may lead to unexpected delays, contributor attrition, and a lost of trust in the project.

* Inactivity is measured by:
    * Periods of no contributions for longer than 6 months
    * Periods of no communication for longer than 3 months
* Consequences of being inactive include:
    * Involuntary removal or demotion
    * Being asked to move to Emeritus status

## Involuntary Removal or Demotion

Involuntary removal/demotion of a contributor happens when responsibilities and requirements aren't being met. This may include repeated patterns of inactivity, extended period of inactivity, a period of failing to meet the requirements of your role, and/or a violation of the Code of Conduct. This process is important because it protects the community and its deliverables while also opens up opportunities for new contributors to step in.

Involuntary removal or demotion is handled through a vote by a majority of the current Maintainers.

## Stepping Down/Emeritus Process
If and when contributors' commitment levels change, contributors can consider stepping down (moving down the contributor ladder) vs moving to emeritus status (completely stepping away from the project).

Contact the Maintainers about changing to Emeritus status, or reducing your contributor level.

## Contact
* For inquiries, please reach out to: @kvaps, @tym83
