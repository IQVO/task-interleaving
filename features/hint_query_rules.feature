Feature: Reading hints and stations, and the limits of the list
  Hints are listed newest first and keyset paged, limit 1 to 500; every filter and
  id is validated before anything is read; an unknown hint or a station without a
  recorded completion is not found.

  Background:
    Given site "SITE-1" has these locations:
      | code | x | y | z |
      | L0   | 0 | 0 | 0 |
      | A    | 3 | 0 | 0 |
    And site "SITE-2" has these locations:
      | code | x | y | z |
      | S2L  | 0 | 0 | 0 |
      | S2A  | 1 | 0 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And site "SITE-2" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-s2" of type "PICK" is available at site "SITE-2" from "S2A" to "S2A"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T0b" of type "PICK" is available at site "SITE-2" from "S2L" to "S2L"
    And station "STN-2" claims task "T0b"
    And task "T0c" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-3" claims task "T0c"
    And task "T0" is completed
    And 10 seconds pass
    And task "T0b" is completed
    And 10 seconds pass
    And task "T0c" is completed

  Scenario: Hints are listed newest first and a page that exactly fills the limit has no next cursor
    When I list the hints with limit 3
    Then the page lists hint numbers "3,2,1"
    And the page has no next cursor
    When I list the hints with limit 500
    Then the page lists hint numbers "3,2,1"

  Scenario: A page that is full but not last hands out a cursor to the rest
    When I list the hints with limit 2
    Then the page lists hint numbers "3,2"
    And the page has a next cursor
    When I list the hints with limit 2 after the cursor of the previous page
    Then the page lists hint numbers "1"
    And the page has no next cursor

  Scenario: The list can be narrowed by station, by site and by state
    When I list the hints of station "STN-2"
    Then the page lists hint numbers "2"
    When I list the hints of site "SITE-1"
    Then the page lists hint numbers "3,1"
    Given station "STN-3" claims task "T-1"
    When I list the hints in state "Consumed"
    Then the page lists hint numbers "3"
    When I list the hints in state "Active"
    Then the page lists hint numbers "2,1"
    When I list the hints in state "Superseded"
    Then the page lists hint numbers ""

  Scenario Outline: A malformed list query is refused before anything is read
    When I list the hints <query>
    Then the command fails with "<message>"

    Examples: limit 1 to 500, one of the four states, a cursor this API handed out, valid ids
      | query                    | message                                 |
      | with limit 501           | limit must be between 1 and 500         |
      | with limit -1            | limit must be between 1 and 500         |
      | in state "Archived"      | state must be one of                    |
      | in state "active"        | state must be one of                    |
      | with the cursor "***"    | cursor is not a value returned by this API |
      | of station "bad id"      | invalid station id                      |
      | of site "SITE/1"         | invalid site id                         |

  Scenario Outline: A hint or a station is read by a valid id and exists once recorded
    When I read <what>
    Then the command fails with "<message>"

    Examples: hint ids start with hint-, a station exists after its first recorded completion
      | what              | message                     |
      | hint "abc"        | invalid hint id             |
      | hint "hint-"      | invalid hint id             |
      | hint "hint-a/b"   | invalid hint id             |
      | hint "hint-9999"  | interleaving hint not found |
      | station "bad id"  | invalid station id          |
      | station "STN-404" | station not found           |
