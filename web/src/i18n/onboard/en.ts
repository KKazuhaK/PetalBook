import type { OnboardMessages } from '../onboardTypes.ts'

export const onboardEn: OnboardMessages = {
  help: {
    label: 'Help',
    tour: 'Take the tour',
    repo: 'Connect your own repository, step by step',
    admin: 'For administrators',
  },
  tour: {
    title: 'Getting started',
    step: (i: number, n: number) => `Step ${i} of ${n}`,
    next: 'Next',
    back: 'Back',
    skip: 'Skip',
    finish: 'Start using it',
    skipHelp: 'You can skip it at any time, and open it again from Help at the bottom left.',
    steps: {
      welcome: {
        title: 'Welcome to PetalBook',
        body: 'A minute to see how to write your first page and find it again. Press Skip if you would rather not; Help at the bottom left opens this again.',
      },
      page: {
        title: 'Make a page',
        body: 'Press Quick note in the sidebar to jot something down, or press New inside a folder. Every page is a plain Markdown file in the space’s repository: nothing is locked inside the app.',
        action: 'Open quick note',
      },
      editor: {
        title: 'Writing',
        body: 'It saves as you type. Selecting text shows the formatting toolbar; the switch at the top right moves to source mode; tags, sources and the other facts of a page are changed in its properties panel, which only lets you change values, so the header cannot be broken.',
      },
      favorites: {
        title: 'Favourites',
        body: 'Hover a row of a folder’s list and a star fades in on the right; press it and the page is a favourite, listed at the top of the sidebar. On a phone the star is always there, only fainter.',
      },
      search: {
        title: 'Search',
        body: 'Press Ctrl+K (⌘K on a Mac), or the search box at the top of the sidebar. Search by title, text, tag and source, and narrow by date.',
        action: 'Open search',
      },
      repo: {
        title: 'Connect your own repository',
        bodyAsked:
          'This site would like your space to follow a Git repository of your own, so your writing is also in your hands. Two steps are yours to take; they are explained one at a time below.',
        bodyForced:
          'This site requires your space to follow a Git repository of your own, and the space is read-only until it does. Two steps are yours to take; they are explained one at a time below.',
        action: 'Step by step',
      },
      admin: {
        title: 'Administrators: first things first',
        body: 'The daily text is off by default: open Administration → Daily text and switch on a built-in interface, or add a few lines of your own, and the home page shows one each day. People, storage and sign-in are in the administration too.',
        action: 'Open daily text',
      },
      done: {
        title: 'That is all',
        body: 'To see it again, press Help at the bottom of the sidebar. Happy writing.',
      },
    },
  },
  repo: {
    title: 'Connect your own repository',
    whyTitle: 'Why',
    whyAsked: (when: string) =>
      `This site suggests that your space follows a Git repository of your own, so a copy of your writing stays in your hands. Connect it before ${when}; writing is not affected now.`,
    whyForced:
      'This site requires your space to follow a Git repository of your own. Until it does, the space is read-only; once it does, you can write again at once.',
    byProvider: (name: string) => `It has to be on ${name}.`,
    anyProvider: 'Any platform this site supports will do.',
    stepsTitle: 'What to do',
    stepOf: (i: number, n: number) => `Step ${i} of ${n}`,
    done: 'Done',
    todo: 'Not done yet',
    now: 'Do this one now',
    auto: 'Happens by itself',
    connect: {
      title: (name: string) => `Connect your ${name} account`,
      what: (name: string) =>
        `Press Connect, choose ${name} under Connections in Settings, and you are taken to ${name}’s authorisation page.`,
      authorise: (name: string) =>
        `Authorise PetalBook to read your account on ${name}. This does not give it access to any repository.`,
      action: 'Connect',
    },
    repo: {
      title: 'Choose a repository, or let PetalBook make one',
      what: 'Press Open sync settings, then Connect a repository: choose an empty repository you have, or let PetalBook make a new one under your account.',
      authorise:
        'On the platform’s authorisation page, give PetalBook read and write access (contents) to this one repository only; it does not need all of them.',
      action: 'Open sync settings',
    },
    sync: {
      title: 'Wait for the first sync',
      what: 'Once connected, the space syncs once, and when its state says Synced you are done. There is nothing more to do.',
    },
    left: (n: number) => (n === 1 ? 'One step left' : `${n} steps left`),
    helpTitle: 'Stuck?',
    help: 'Read the hint on the Sync page of the settings first: it says what is wrong. If that does not help, send the error text on the page to your administrator.',
    nothing:
      'No space needs connecting right now: the site does not ask for it, or your spaces are connected already. You can connect one yourself at any time under Sync in the settings.',
    close: 'Close',
    bannerLeft: (step: string) => `Still to do: ${step}`,
    bannerAction: 'Step by step',
  },
}
