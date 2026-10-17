Feature: The task pool and the claims behind the hints
  The open-task pool is fed by TaskAvailable and TaskClaimed events and the end
  position of a completed task is read from the local copy of the task. A
  consumer never crashes on missing local data: it records what it can and
  ignores the rest.

  Background:
    Given site "SITE-1" has these locations:
      | code | x  | y | z |
      | L0   | 0  | 0 | 0 |
      | A    | 3  | 0 | 0 |
      | B    | 3  | 4 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "B" to "B"

  Scenario: A task claimed by someone else is no longer a candidate
    Given station "STN-2" claims task "T-1"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-2"

  Scenario: A task that re-enters the pool after its lease expired is a candidate again
    Given station "STN-2" claims task "T-1"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-1,T-2"

  Scenario: A completed task never re-enters the pool
    Given station "STN-2" claims task "T-1"
    And task "T-1" is completed
    When task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    Then the consumer outcome is "stale"
    When 10 seconds pass
    And task "T0" is completed
    Then station "STN-1" has an active hint listing "T-2"

  Scenario: The completion of a task the service never saw is recorded and ignored
    When task "T-ghost" is completed
    Then the consumer outcome is "ignored"
    And station "STN-9" is unknown
    And 0 hints were issued

  Scenario: The completion of a task nobody claimed is ignored
    When task "T-1" is completed
    Then the consumer outcome is "ignored"
    And 0 hints were issued

  Scenario: The claim of a task the service never saw is recorded without a position
    When station "STN-1" claims task "T-ghost"
    Then the consumer outcome is "applied"
    When task "T-ghost" is completed
    Then the consumer outcome is "ignored"
    And station "STN-1" is unknown

  Scenario: A task that does not say where it ends cannot move its station
    Given task "T7" of type "PICK" is available at site "SITE-1" from "A" to nowhere
    And station "STN-1" claims task "T7"
    When task "T7" is completed
    Then the consumer outcome is "ignored"
    And station "STN-1" is unknown

  Scenario: A task without a site is an invalid event
    When task "T8" is available without a site
    Then the command fails with "invalid event"

  Scenario: An availability event delivered twice is applied once
    Given task "T9" of type "PICK" is available at site "SITE-1" from "A" to "A"
    When the last event is delivered again
    Then the consumer outcome is "duplicate"

  Scenario: A layout event delivered twice is applied once
    Given the location "N" of site "SITE-1" is registered without geometry
    When the last event is delivered again
    Then the consumer outcome is "duplicate"
